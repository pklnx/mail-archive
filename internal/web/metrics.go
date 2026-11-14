package web

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/keepia/archive/internal/store"
)

// GET /api/metrics (#397, part 3) gives the instance's figures in the
// Prometheus text format 0.0.4, written by hand. Admins read it with a
// session or with an API key whose only scope is metrics. Scrapes often
// end in third-party monitoring, so it holds IDs, counts, times and fixed
// enums only: no name, host, IMAP user name, address, folder, subject or
// error text. Two layers keep it so: metricsInput has no field that could
// hold such a value (a reflection test checks every field), and every
// label value is checked against its label's pattern when written; a
// value that does not match is dropped with its sample.

// metricsTTL is how long the answer is served from memory, so that scrapes
// cannot load the database.
const metricsTTL = 15 * time.Second

// metricsContentType is the Prometheus text format.
const metricsContentType = "text/plain; version=0.0.4; charset=utf-8"

// metricsBackupLookBack is how far back the last backup is looked for in the
// audit log.
const metricsBackupLookBack = 30 * 24 * time.Hour

type metricsCache struct {
	mu   sync.Mutex
	at   time.Time
	body []byte
}

// metricsInput is everything the metrics are written from.
type metricsInput struct {
	DB *store.InstanceMetrics
	// QuotaBytes is the instance quota, 0 without one.
	QuotaBytes int64
	// Health is the health of each account by ID (store.HealthOK,
	// HealthFailing, HealthStale): its syncs, and its recurring fills.
	Health map[int64]string
	// Version is the binary's version; SchemaVersion, LatestVersion and
	// Pending the migrations.
	Version                      string
	SchemaVersion, LatestVersion int64
	Pending                      int
	// Encryption is the state of encryption at rest ("off", "on", or a
	// conversion in progress).
	Encryption string
	// Disk is false when the data disk could not be measured.
	Disk                 bool
	DiskFree, DiskTotal  uint64
	LastBackupAt         *time.Time
	SMTPStorageDeferrals uint64
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	now := s.currentTime()
	c := &s.metricsCache
	c.mu.Lock()
	if c.body == nil || now.Sub(c.at) >= metricsTTL || now.Before(c.at) {
		in, err := s.metricsInput(r.Context(), now)
		if err != nil {
			c.mu.Unlock()
			s.failStore(w, r, err)
			return
		}
		c.body, c.at = writeMetrics(in), now
	}
	body := c.body
	c.mu.Unlock()
	w.Header().Set("Content-Type", metricsContentType)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

// metricsInput reads the figures.
func (s *Server) metricsInput(ctx context.Context, now time.Time) (*metricsInput, error) {
	in := &metricsInput{QuotaBytes: s.storageQuota, Version: s.version}
	var err error
	if in.DB, err = s.store.InstanceMetrics(ctx); err != nil {
		return nil, err
	}
	if in.Health, err = s.accountHealth(ctx); err != nil {
		return nil, err
	}
	is, err := s.store.InstanceStatus(ctx, s.version)
	if err != nil {
		return nil, err
	}
	in.SchemaVersion, in.LatestVersion, in.Pending = is.SchemaVersion, is.LatestVersion, len(is.Pending)
	in.Encryption = is.EncryptionAtRest
	if in.Encryption == store.BlobOff {
		in.Encryption = "off"
	}
	if s.diskUsage != nil {
		free, total, err := s.diskUsage()
		if err != nil {
			s.log.Warn("metrics: measure the data disk", "err", err)
		} else {
			in.Disk, in.DiskFree, in.DiskTotal = true, free, total
		}
	}
	if in.LastBackupAt, err = s.store.LastBackupSince(ctx, now.Add(-metricsBackupLookBack)); err != nil {
		return nil, err
	}
	if s.smtpStorageDeferrals != nil {
		in.SMTPStorageDeferrals = s.smtpStorageDeferrals()
	}
	return in, nil
}

// accountHealth is the health of each account by ID, as /healthz/sync
// sees it: an IMAP account's syncs, and the worst of an smtp account's
// recurring fills.
func (s *Server) accountHealth(ctx context.Context) (map[int64]string, error) {
	health, err := s.store.SyncHealth(ctx, nil)
	if err != nil {
		return nil, err
	}
	fills, err := s.store.FillHealth(ctx, nil)
	if err != nil {
		return nil, err
	}
	busy, err := s.loadBusy(ctx)
	if err != nil {
		return nil, err
	}
	now := s.currentTime()
	out := make(map[int64]string, len(health))
	for id, h := range health {
		out[id] = s.healthState(h, busy, now)
	}
	for _, h := range fills {
		out[h.AccountID] = worseHealth(out[h.AccountID], s.fillHealthState(h, busy, now))
	}
	return out, nil
}

// worseHealth is the worse of two health states; "" counts as ok.
func worseHealth(a, b string) string {
	rank := map[string]int{store.HealthOK: 1, store.HealthStale: 2, store.HealthFailing: 3}
	if rank[b] > rank[a] {
		return b
	}
	if a == "" {
		return store.HealthOK
	}
	return a
}

// Label patterns: every label a metric may carry, and what its values may
// look like. A label not listed here cannot be written.
var metricLabels = map[string]*regexp.Regexp{
	"account_id": regexp.MustCompile(`^[0-9]{1,19}$`),
	"group_id":   regexp.MustCompile(`^[0-9]{1,19}$`),
	"scope":      regexp.MustCompile(`^(instance|group|ungrouped)$`),
	"kind":       regexp.MustCompile(`^(imap|smtp|import|admin|member|auditor)$`),
	"health":     regexp.MustCompile(`^(ok|failing|stale)$`),
	"state":      regexp.MustCompile(`^(ok|warn|full|overflow|disk_low|off|on|encrypting|decrypting|rekeying|active|locked|queued|retrying|running|failed)$`),
	"table":      regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`),
	"job_kind":   regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`),
	"version":    regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$`),
}

// Quota states, in order, and those a group can have.
var (
	storageStates = []string{"ok", "warn", "full", "overflow", "disk_low"}
	groupStates   = []string{"ok", "warn", "full", "overflow"}
	healthStates  = []string{store.HealthOK, store.HealthFailing, store.HealthStale}
	atRestStates  = []string{"off", store.BlobOn, store.BlobEncrypting, store.BlobDecrypting, store.BlobRekeying}
)

// metricLabel is one label of a sample.
type metricLabel struct{ name, value string }

// metricsWriter writes metric families in the text format. A family's
// HELP and TYPE come before its first sample.
type metricsWriter struct {
	buf     bytes.Buffer
	current string
}

func (m *metricsWriter) family(name, typ, help string) {
	m.current = "mail_archive_" + name
	fmt.Fprintf(&m.buf, "# HELP %s %s\n# TYPE %s %s\n", m.current, help, m.current, typ)
}

// sample writes one sample of the current family. It is dropped when a
// label is unknown or its value does not match the label's pattern.
func (m *metricsWriter) sample(value float64, labels ...metricLabel) {
	var b strings.Builder
	b.WriteString(m.current)
	if len(labels) > 0 {
		b.WriteByte('{')
		for i, l := range labels {
			p, ok := metricLabels[l.name]
			if !ok || !p.MatchString(l.value) {
				return
			}
			if i > 0 {
				b.WriteByte(',')
			}
			// The patterns admit no quote, backslash or line break, so
			// the value needs no escaping.
			b.WriteString(l.name + `="` + l.value + `"`)
		}
		b.WriteByte('}')
	}
	b.WriteByte(' ')
	b.WriteString(strconv.FormatFloat(value, 'g', -1, 64))
	b.WriteByte('\n')
	m.buf.WriteString(b.String())
}

func metricID(n int64) string { return strconv.FormatInt(n, 10) }

func unixSeconds(t time.Time) float64 { return float64(t.UnixMilli()) / 1000 }

func boolValue(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// writeMetrics writes in in the text format.
func writeMetrics(in *metricsInput) []byte {
	var m metricsWriter
	db := in.DB
	instance := metricLabel{"scope", "instance"}

	m.family("build_info", "gauge", "The version of the running binary.")
	m.sample(1, metricLabel{"version", in.Version})
	m.family("schema_version", "gauge", "The highest migration the database has applied.")
	m.sample(float64(in.SchemaVersion))
	m.family("schema_latest_version", "gauge", "The highest migration this binary has.")
	m.sample(float64(in.LatestVersion))
	m.family("migrations_pending", "gauge", "Migrations not applied yet.")
	m.sample(float64(in.Pending))

	m.family("storage_messages", "gauge", "Unique messages: exact for the instance, as of the last snapshot plus what was added since for a group.")
	m.sample(float64(db.Messages), instance)
	for _, g := range db.Groups {
		m.sample(float64(g.Messages), metricLabel{"scope", "group"}, metricLabel{"group_id", metricID(g.ID)})
	}
	m.sample(float64(db.UngroupedMessages), metricLabel{"scope", "ungrouped"})
	m.family("storage_bytes", "gauge", "Message bytes after deduplication (plaintext sizes), as storage_messages.")
	m.sample(float64(db.Bytes), instance)
	for _, g := range db.Groups {
		m.sample(float64(g.Bytes), metricLabel{"scope", "group"}, metricLabel{"group_id", metricID(g.ID)})
	}
	m.sample(float64(db.UngroupedBytes), metricLabel{"scope", "ungrouped"})
	m.family("storage_quota_bytes", "gauge", "The quota of the instance or a group; absent without one.")
	if in.QuotaBytes > 0 {
		m.sample(float64(in.QuotaBytes), instance)
	}
	for _, g := range db.Groups {
		if g.QuotaBytes != nil {
			m.sample(float64(*g.QuotaBytes), metricLabel{"scope", "group"}, metricLabel{"group_id", metricID(g.ID)})
		}
	}
	m.family("storage_state", "gauge", "The quota state of the instance or a group: 1 for the current state.")
	for _, st := range storageStates {
		m.sample(boolValue(db.State == st), instance, metricLabel{"state", st})
	}
	for _, g := range db.Groups {
		for _, st := range groupStates {
			m.sample(boolValue(g.State == st), metricLabel{"scope", "group"}, metricLabel{"group_id", metricID(g.ID)}, metricLabel{"state", st})
		}
	}
	m.family("storage_state_since_timestamp_seconds", "gauge", "When the quota state of the instance or a group began.")
	m.sample(unixSeconds(db.StateSince), instance)
	for _, g := range db.Groups {
		if g.StateSince != nil {
			m.sample(unixSeconds(*g.StateSince), metricLabel{"scope", "group"}, metricLabel{"group_id", metricID(g.ID)})
		}
	}
	m.family("storage_snapshot_timestamp_seconds", "gauge", "When the last usage snapshot ran; 0 before the first.")
	if db.SnapshotAt != nil {
		m.sample(unixSeconds(*db.SnapshotAt))
	} else {
		m.sample(0)
	}
	m.family("storage_counter_drift_bytes", "gauge", "The last snapshot's difference between the counters and an exact count; 0 is right.")
	m.sample(float64(db.CounterDrift))

	m.family("account_messages", "gauge", "Messages of an account at the last snapshot.")
	for _, a := range db.Accounts {
		m.sample(float64(a.Messages), accountLabels(a)...)
	}
	m.family("account_bytes", "gauge", "Message bytes of an account at the last snapshot; shared messages count in every account that holds them.")
	for _, a := range db.Accounts {
		m.sample(float64(a.Bytes), accountLabels(a)...)
	}
	m.family("account_enabled", "gauge", "1 when the account is enabled.")
	for _, a := range db.Accounts {
		m.sample(boolValue(a.Enabled), metricLabel{"account_id", metricID(a.ID)})
	}
	m.family("account_health", "gauge", "The health of an account's syncs or recurring fills: 1 for the current state.")
	for _, a := range db.Accounts {
		h := in.Health[a.ID]
		if h == "" {
			h = store.HealthOK
		}
		for _, st := range healthStates {
			m.sample(boolValue(h == st), metricLabel{"account_id", metricID(a.ID)}, metricLabel{"health", st})
		}
	}
	m.family("account_last_success_timestamp_seconds", "gauge", "The last successful sync of an IMAP account; absent before the first.")
	for _, a := range db.Accounts {
		if a.LastSuccessAt != nil {
			m.sample(unixSeconds(*a.LastSuccessAt), metricLabel{"account_id", metricID(a.ID)})
		}
	}

	if in.Disk {
		m.family("disk_free_bytes", "gauge", "Free bytes on the data directory's file system, for the app's user.")
		m.sample(float64(in.DiskFree))
		m.family("disk_total_bytes", "gauge", "Size of the data directory's file system.")
		m.sample(float64(in.DiskTotal))
	}
	m.family("database_bytes", "gauge", "The database's size at the last snapshot.")
	m.sample(float64(db.DatabaseBytes))
	m.family("database_relation_bytes", "gauge", "The size of a table with its indexes and TOAST at the last snapshot.")
	tables := make([]string, 0, len(db.RelationBytes))
	for t := range db.RelationBytes {
		tables = append(tables, t)
	}
	sort.Strings(tables)
	for _, t := range tables {
		m.sample(float64(db.RelationBytes[t]), metricLabel{"table", t})
	}

	m.family("jobs", "gauge", "Jobs per kind and state (done jobs are not counted).")
	for _, j := range db.Jobs {
		for _, c := range []struct {
			state string
			n     int64
		}{{"queued", j.Queued}, {"retrying", j.Retrying}, {"running", j.Running}, {"failed", j.Failed}} {
			m.sample(float64(c.n), metricLabel{"job_kind", j.Kind}, metricLabel{"state", c.state})
		}
	}
	m.family("job_oldest_due_seconds", "gauge", "How long the oldest due job of a kind has been due; 0 when none is.")
	for _, j := range db.Jobs {
		m.sample(j.OldestDue.Seconds(), metricLabel{"job_kind", j.Kind})
	}

	m.family("users", "gauge", "Users per role (kind) and lock state.")
	for _, u := range db.Users {
		st := "active"
		if u.Locked {
			st = "locked"
		}
		m.sample(float64(u.N), metricLabel{"kind", u.Role}, metricLabel{"state", st})
	}
	m.family("encryption_state", "gauge", "Encryption at rest: 1 for the current state.")
	for _, st := range atRestStates {
		m.sample(boolValue(in.Encryption == st), metricLabel{"state", st})
	}
	m.family("last_backup_timestamp_seconds", "gauge", "The newest backup in the audit log; 0 when none in the last 30 days.")
	if in.LastBackupAt != nil {
		m.sample(unixSeconds(*in.LastBackupAt))
	} else {
		m.sample(0)
	}
	m.family("smtp_storage_deferrals_total", "counter", "SMTP recipients deferred with 452 for storage since the start.")
	m.sample(float64(in.SMTPStorageDeferrals))
	return m.buf.Bytes()
}

// accountLabels are the labels of an account's storage samples.
func accountLabels(a store.AccountMetrics) []metricLabel {
	out := []metricLabel{{"account_id", metricID(a.ID)}}
	if a.GroupID != nil {
		out = append(out, metricLabel{"group_id", metricID(*a.GroupID)})
	}
	return append(out, metricLabel{"kind", string(a.Kind)})
}
