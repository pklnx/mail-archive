package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
	"github.com/pklnx/mail-archive/internal/testwait"
)

// TestUsageCommand (#397, part 1): before the first snapshot the figures
// show "-"; then the instance, each group with its quota and state, the
// "Without group" row while an account has no owner, and each account with
// owner, group and exclusive bytes, as text and JSON; --group filters;
// status gets a storage line.
func TestUsageCommand(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	t.Setenv("MAIL_ARCHIVE_DATABASE_URL", url)
	ctx := context.Background()
	run := func(args ...string) string {
		t.Helper()
		var err error
		s := captureStdout(t, func() { err = runCmd(t, newUsageCmd(), "", args...) })
		if err != nil {
			t.Fatalf("usage %v: %v\n%s", args, err, s)
		}
		return s
	}
	save := func(account int64, uid uint32, sha string, size int64) {
		t.Helper()
		fo, err := st.GetOrCreateFolder(ctx, account, "INBOX")
		if err != nil {
			t.Fatal(err)
		}
		meta := store.MessageMeta{SHA256: strings.Repeat(sha, 64), Size: size, StoredPath: "x"}
		if _, err := st.SaveBatch(ctx, fo.ID, uid, []store.MessageMeta{meta}, []store.Location{{FolderID: fo.ID, UID: uid}}); err != nil {
			t.Fatal(err)
		}
	}
	// Before the first user: an account without an owner.
	early := &store.Account{Kind: store.KindImport, Name: "early"}
	if err := st.CreateAccount(ctx, early); err != nil {
		t.Fatal(err)
	}
	save(early.ID, 1, "a", 2048)
	out := run()
	for _, re := range []string{`instance: 1 messages, 2\.0 KiB \(exact\), state ok`, "not counted yet",
		`Default\s+-\s+-\s+-\s+ok`, `Without group\s+-\s+-\s+-\s+-`, `early\s+import\s+-\s+Without group\s+-\s+-\s+-`} {
		if !regexp.MustCompile(re).MatchString(out) {
			t.Errorf("before the snapshot, no %s in:\n%s", re, out)
		}
	}
	if _, err := st.RefreshUsage(ctx); err != nil {
		t.Fatal(err)
	}
	out = run()
	if !regexp.MustCompile(`Without group\s+1\s+2\.0 KiB\s+-\s+-`).MatchString(out) || !strings.Contains(out, "counted at ") {
		t.Errorf("without group after the snapshot:\n%s", out)
	}
	var v usageJSON
	if err := json.Unmarshal([]byte(run("--json")), &v); err != nil {
		t.Fatal(err)
	}
	if v.WithoutGroup == nil || *v.WithoutGroup != (countJSON{Messages: 1, Bytes: 2048}) || v.Messages != 1 || v.SnapshotAt == nil ||
		len(v.Accounts) != 1 || v.Accounts[0].Owner != nil || v.Accounts[0].Group != nil {
		t.Errorf("JSON before the first user: %+v", v)
	}

	// The first user adopts it; a second group with a quota and a user.
	anna, err := st.CreateUser(ctx, "anna", "h", false)
	if err != nil {
		t.Fatal(err)
	}
	meier, err := st.CreateGroup(ctx, "Meier")
	if err != nil {
		t.Fatal(err)
	}
	ben, err := st.CreateUserInGroup(ctx, store.NewUser{Name: "ben", PasswordHash: "h", GroupID: meier.ID})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, "UPDATE customer_groups SET quota_bytes = 1073741824 WHERE id = $1", meier.ID); err != nil {
		t.Fatal(err)
	}
	mail := &store.Account{Kind: store.KindImport, Name: "mail", OwnerID: &ben.ID}
	if err := st.CreateAccount(ctx, mail); err != nil {
		t.Fatal(err)
	}
	save(mail.ID, 1, "a", 2048)
	save(mail.ID, 2, "b", 100)
	if _, err := st.RefreshUsage(ctx); err != nil {
		t.Fatal(err)
	}
	out = run()
	for _, re := range []string{
		`instance: 2 messages, 2\.1 KiB \(exact\), state ok`,
		`group quotas: 1 set, 1\.0 GiB in total`,
		`Default\s+1\s+2\.0 KiB\s+-\s+ok`,
		`Meier\s+2\s+2\.1 KiB\s+1\.0 GiB\s+ok`,
		`early\s+import\s+anna\s+Default\s+1\s+2\.0 KiB\s+0 B`,
		`mail\s+import\s+ben\s+Meier\s+2\s+2\.1 KiB\s+100 B`,
	} {
		if !regexp.MustCompile(re).MatchString(out) {
			t.Errorf("no %s in:\n%s", re, out)
		}
	}
	if strings.Contains(out, "Without group") {
		t.Errorf("a Without group row after the adoption:\n%s", out)
	}
	v = usageJSON{}
	if err := json.Unmarshal([]byte(run("--json", "--group", "meier")), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Groups) != 1 || v.Groups[0].Name != "Meier" || v.Groups[0].QuotaBytes == nil || *v.Groups[0].QuotaBytes != 1<<30 ||
		v.Groups[0].Bytes != 2148 || len(v.Accounts) != 1 || *v.Accounts[0].Owner != "ben" || *v.Accounts[0].Group != "Meier" ||
		v.Accounts[0].ExclusiveBytes != 100 || v.WithoutGroup != nil || v.RelationBytes["messages"] <= 0 {
		t.Errorf("JSON of Meier: %+v", v)
	}
	if err := runCmd(t, newUsageCmd(), "", "--group", "nosuch"); err == nil || !strings.Contains(err.Error(), `no group named "nosuch"`) {
		t.Errorf("unknown group: %v", err)
	}
	_ = anna
	status := captureStdout(t, func() {
		if err := runCmd(t, newStatusCmd(), ""); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(status, "storage: 2 unique messages, 2.1 KiB; per group and account: ") {
		t.Errorf("status:\n%s", status)
	}
}

// TestStorageSchedule: the schedule enqueues storage_fold at start, and
// the first snapshot while none was taken; then storage_fold and
// usage_refresh on their ticks (a fake clock), also when they failed
// before.
func TestStorageSchedule(t *testing.T) {
	st, conn := jobsEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fold, refresh := make(chan time.Time), make(chan time.Time)
	kinds := make(chan string, 4)
	s := &storageSchedule{store: st, log: slog.New(slog.NewTextHandler(io.Discard, nil)), fold: fold, refresh: refresh,
		enqueued: func(kind string) { kinds <- kind }}
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.run(ctx)
	}()
	next := func(want string) {
		t.Helper()
		select {
		case k := <-kinds:
			if k != want {
				t.Fatalf("enqueued %s, want %s", k, want)
			}
		case <-time.After(testwait.Limit(t)):
			t.Fatalf("%s was not enqueued", want)
		}
	}
	state := func(kind string) string {
		t.Helper()
		var s string
		if err := conn.QueryRow(context.Background(), "SELECT state FROM jobs WHERE kind = $1 AND key = 'singleton'", kind).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	next(store.JobStorageFold)
	next(store.JobUsageRefresh)
	if state(store.JobStorageFold) != store.JobQueued || state(store.JobUsageRefresh) != store.JobQueued {
		t.Fatal("not queued at start")
	}
	for _, kind := range []string{store.JobStorageFold, store.JobUsageRefresh} {
		if _, err := conn.Exec(ctx, "UPDATE jobs SET state = 'failed', finished_at = now() WHERE kind = $1", kind); err != nil {
			t.Fatal(err)
		}
	}
	fold <- time.Now()
	next(store.JobStorageFold)
	if state(store.JobStorageFold) != store.JobQueued || state(store.JobUsageRefresh) != store.JobFailed {
		t.Errorf("after the fold tick: fold %s, refresh %s", state(store.JobStorageFold), state(store.JobUsageRefresh))
	}
	refresh <- time.Now()
	next(store.JobUsageRefresh)
	if state(store.JobUsageRefresh) != store.JobQueued {
		t.Errorf("after the hourly tick: refresh %s", state(store.JobUsageRefresh))
	}
	cancel()
	<-done

	// With a snapshot taken, a start queues only the fold.
	if _, err := st.RefreshUsage(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(context.Background(), "UPDATE jobs SET state = 'done', finished_at = now()"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	done = make(chan struct{})
	go func() {
		defer close(done)
		s.run(ctx)
	}()
	next(store.JobStorageFold)
	fold <- time.Now() // taken only once the start is over
	next(store.JobStorageFold)
	cancel()
	<-done
	select {
	case k := <-kinds:
		t.Errorf("enqueued %s at start with a snapshot", k)
	default:
	}
	if state(store.JobUsageRefresh) != store.JobDone {
		t.Errorf("refresh %s", state(store.JobUsageRefresh))
	}
}
