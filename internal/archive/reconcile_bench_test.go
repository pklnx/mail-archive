package archive_test

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/imaptest"
	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

const benchServerEnv = "MAIL_ARCHIVE_BENCH_SERVER"

// TestReconcileCost reconciles folders of 100k and 500k messages, one
// percent of them deleted and one percent with new flags. The IMAP server
// runs in a child process, so the reported peak heap is the archive's.
func TestReconcileCost(t *testing.T) {
	if os.Getenv("MAIL_ARCHIVE_BENCH") != "1" {
		t.Skip("set MAIL_ARCHIVE_BENCH=1 to run")
	}
	for _, n := range []int{100_000, 500_000} {
		t.Run(strconv.Itoa(n), func(t *testing.T) { reconcileCost(t, n) })
	}
}

// TestReconcileBenchServer is the child process of TestReconcileCost: it
// serves n messages and prints its port, until stdin closes.
func TestReconcileBenchServer(t *testing.T) {
	n, err := strconv.Atoi(os.Getenv(benchServerEnv))
	if err != nil {
		t.Skip("only run by TestReconcileCost")
	}
	u := imapmemserver.NewUser("bench", "pw")
	createMailboxes(t, u, "INBOX")
	raw := []byte("Subject: x\r\n\r\nx\r\n")
	for uid := 1; uid <= n; uid++ {
		opts := &imap.AppendOptions{}
		switch {
		case uid%100 == 50:
			opts.Flags = []imap.Flag{imap.FlagFlagged} // the archive has \Seen or nothing
		case uid%3 == 1:
			opts.Flags = []imap.Flag{imap.FlagSeen}
		}
		if _, err := u.Append("INBOX", bytes.NewReader(raw), opts); err != nil {
			t.Fatal(err)
		}
	}
	srv := imaptest.StartServer(t, u)
	for uid := 1; uid <= n; uid += 100 {
		srv.Hide("INBOX", uint32(uid)) //nolint:gosec // test sizes
	}
	fmt.Printf("PORT=%d\n", srv.Port)
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n') // until the parent closes stdin
}

func reconcileCost(t *testing.T, n int) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestReconcileBenchServer$", "-test.v") //nolint:gosec // the test binary itself
	cmd.Env = append(os.Environ(), benchServerEnv+"="+strconv.Itoa(n))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close(); _ = cmd.Wait() }()
	port := 0
	sc := bufio.NewScanner(stdout)
	for port == 0 && sc.Scan() {
		if p, ok := strings.CutPrefix(sc.Text(), "PORT="); ok {
			port, _ = strconv.Atoi(p)
		}
	}
	if port == 0 {
		t.Fatal("bench server did not start")
	}
	go func() {
		for sc.Scan() { // drain
		}
	}()

	f := newFixture(t)
	st, url := storetest.NewWithURL(t)
	f.store, f.syncer.Store = st, st
	f.host, f.port = "127.0.0.1", port
	f.addAccount("bench", "bench", "pw")
	acc, err := f.account("bench")
	if err != nil {
		t.Fatal(err)
	}
	folder, err := st.GetOrCreateFolder(f.ctx, acc.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	ic := imaptest.Client(t, f.host, f.port, "bench", "pw")
	sel, err := ic.Select("INBOX", nil).Wait()
	_ = ic.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.ResetFolder(f.ctx, folder.ID, sel.UIDValidity); err != nil {
		t.Fatal(err)
	}
	// The archive already holds every message, as after a first sync.
	conn, err := pgx.Connect(f.ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(f.ctx) }()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO messages (sha256, size, stored_path)
		  SELECT lpad(to_hex(i), 64, '0'), 1, 'x' FROM generate_series(1, $1::int) i`, []any{n}},
		{`INSERT INTO message_locations (message_sha256, folder_id, uidvalidity, uid, flags)
		  SELECT lpad(to_hex(i), 64, '0'), $2, $3, i, CASE WHEN i % 3 = 1 THEN ARRAY['\Seen'] ELSE '{}' END
		  FROM generate_series(1, $1::int) i`, []any{n, folder.ID, int64(sel.UIDValidity)}},
		{`UPDATE folders SET last_uid = $1 WHERE id = $2`, []any{n, folder.ID}},
		{`VACUUM ANALYZE message_locations`, nil},
	} {
		if _, err := conn.Exec(f.ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}

	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	var peak atomic.Uint64
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		var m runtime.MemStats
		for {
			runtime.ReadMemStats(&m)
			peak.Store(max(peak.Load(), m.HeapInuse))
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
			}
		}
	}()
	start := time.Now()
	r := f.syncer.SyncAccountWith(f.ctx, acc, archive.SyncOptions{Reconcile: true})
	elapsed := time.Since(start)
	close(stop)
	<-done
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if r.Err != nil {
		t.Fatal(r.Err)
	}
	want := store.ReconcileCounts{Folders: 1, Gone: n / 100, FlagsChanged: n / 100}
	if r.Reconcile != want {
		t.Fatalf("counts = %+v, want %+v", r.Reconcile, want)
	}
	t.Logf("reconcile of %d messages: %v; peak heap %.0f MB above the %.0f MB before; %.0f MB allocated in total",
		n, elapsed.Round(time.Millisecond), float64(peak.Load()-min(peak.Load(), base.HeapInuse))/1e6,
		float64(base.HeapInuse)/1e6, float64(after.TotalAlloc-base.TotalAlloc)/1e6)
}
