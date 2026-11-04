//go:build linux

package archive_test

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/archive"
	"github.com/pklnx/mail-archive/internal/blobstore"
	"github.com/pklnx/mail-archive/internal/store"
)

// convertGateFilesEnv overrides the number of files of the gate (default
// 10,000); a smaller number measures with less disk space.
const convertGateFilesEnv = "MAIL_ARCHIVE_CONVERT_GATE_FILES"

// Measurement gate of #435: `encryption enable` on 10,000 seeded 50 KB
// message files converts at least 50 files/s, on at most one core, with
// at most 32 MiB of extra memory (the peak resident set above the one
// before the run; asserted without the race detector, whose shadow memory
// it would count). It needs about 1 GB of disk space for a moment (500 MB
// of plaintext, plus one batch of both forms); -short skips it.
func TestConvertGate(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: the gate writes 500 MB")
	}
	n := 10_000
	if s := os.Getenv(convertGateFilesEnv); s != "" {
		var err error
		if n, err = strconv.Atoi(s); err != nil || n < 1 {
			t.Fatalf("%s=%q", convertGateFilesEnv, s)
		}
	}
	e := newArchiveEnv(t)
	kek, _ := e.keygen()
	seedFiles(t, e.data, n, 50_000)

	settle()
	rssBefore := procStatus(t, "VmRSS")
	resetPeak(t)
	var ru0 syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru0)
	start := time.Now()
	res, err := archive.Enable(e.ctx, e.st, e.newBlobs(), e.convertOpts(kek, nil))
	wall := time.Since(start)
	if err != nil || res.State != store.BlobOn || res.Converted != int64(n) {
		t.Fatalf("%+v %v", res, err)
	}
	var ru1 syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru1)
	cpu := time.Duration(ru1.Utime.Nano()-ru0.Utime.Nano()) + time.Duration(ru1.Stime.Nano()-ru0.Stime.Nano())
	peak := procStatus(t, "VmHWM") - rssBefore
	rate := float64(n) / wall.Seconds()
	cores := cpu.Seconds() / wall.Seconds()
	t.Logf("GATE files=%d size=50KB wall=%s rate=%.0f files/s cpu=%.2f cores peak=+%.1f MiB race=%v GOMAXPROCS=%d per100k=%s",
		n, wall.Round(time.Millisecond), rate, cores, float64(peak)/(1<<20), raceEnabled, runtime.GOMAXPROCS(0),
		(time.Duration(float64(wall) * 100_000 / float64(n))).Round(time.Second))
	if rate < 50 {
		t.Errorf("%.0f files/s, want at least 50", rate)
	}
	// One goroutine converts; the garbage collector's background work and
	// the race detector add a little on other threads.
	if cores > 1.2 {
		t.Errorf("%.2f cores, want at most one", cores)
	}
	if !raceEnabled && peak > 32<<20 {
		t.Errorf("peak +%.1f MiB, want at most 32 MiB", float64(peak)/(1<<20))
	}
}

// seedFiles writes n distinct plaintext message files of about size bytes
// directly into messages/ (no fsync: only the conversion is measured).
func seedFiles(t *testing.T, root string, n, size int) {
	t.Helper()
	body := bytes.Repeat([]byte("Sehr geehrte Damen und Herren, anbei die Rechnung. "), size/50)
	for i := range n {
		raw := append(fmt.Appendf(nil, "Subject: Rechnung %d\r\n\r\n", i), body[:size-30]...)
		s := sha256.Sum256(raw)
		h := hex.EncodeToString(s[:])
		p := filepath.Join(root, filepath.FromSlash(blobstore.RelPath(h)))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func settle() {
	runtime.GC()
	debug.FreeOSMemory()
}

// resetPeak resets VmHWM to the current RSS (Linux 4.0 and later).
func resetPeak(t *testing.T) {
	t.Helper()
	if err := os.WriteFile("/proc/self/clear_refs", []byte("5"), 0); err != nil {
		t.Skipf("cannot reset the peak resident set: %v", err)
	}
}

// procStatus reads a field of /proc/self/status in bytes.
func procStatus(t *testing.T, field string) int64 {
	t.Helper()
	f, err := os.Open("/proc/self/status")
	if err != nil {
		t.Skipf("no /proc/self/status: %v", err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), field+":"); ok {
			kb, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return kb << 10
		}
	}
	t.Fatalf("no %s", field)
	return 0
}
