package smtpd

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// certServer serves TLS handshakes with r on loopback until the test ends.
func certServer(t *testing.T, r *CertReloader) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	t.Cleanup(func() {
		_ = l.Close()
		wg.Wait()
	})
	cfg := &tls.Config{GetCertificate: r.GetCertificate, MinVersion: tls.VersionTLS12}
	wg.Go(func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			wg.Go(func() {
				defer func() { _ = c.Close() }()
				_ = tls.Server(c, cfg).Handshake()
			})
		}
	})
	return l.Addr().String()
}

// handshake connects to addr and returns the certificate it saw.
func handshake(t *testing.T, addr string, roots *x509.CertPool) *x509.Certificate {
	t.Helper()
	c, err := tls.Dial("tcp", addr, &tls.Config{RootCAs: roots, ServerName: "archive.test", MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Error(err)
		return nil
	}
	defer func() { _ = c.Close() }()
	return c.ConnectionState().PeerCertificates[0]
}

func parsePEM(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	b, _ := pem.Decode(certPEM)
	c, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

type certFixture struct {
	dir        string
	cert, key  string
	clock      *fakeClock
	stats      atomic.Int64
	logs       *syncBuffer
	roots      *x509.CertPool
	r          *CertReloader
	start      time.Time
	modVersion int
}

// newCertFixture writes a pair valid until notAfter and loads it.
func newCertFixture(t *testing.T, valid time.Duration) (*certFixture, *x509.Certificate) {
	t.Helper()
	// Real time: clients check the validity against the wall clock.
	start := time.Now().UTC().Truncate(time.Second)
	f := &certFixture{dir: t.TempDir(), clock: &fakeClock{t: start}, logs: &syncBuffer{}, roots: x509.NewCertPool(), start: start}
	leaf := f.write(t, start.Add(valid))
	r, err := newCertReloader(f.cert, f.key, slog.New(slog.NewTextHandler(f.logs, nil)), f.clock.Now,
		func(name string) (fs.FileInfo, error) {
			f.stats.Add(1)
			return os.Stat(name)
		})
	if err != nil {
		t.Fatal(err)
	}
	f.r = r
	return f, leaf
}

// write writes a new pair valid until notAfter, with a new modification
// time, and trusts it.
func (f *certFixture) write(t *testing.T, notAfter time.Time) *x509.Certificate {
	t.Helper()
	certPEM, keyPEM := testCert(t, f.start.Add(-24*time.Hour), notAfter)
	f.modVersion++
	f.cert, f.key = writePair(t, f.dir, certPEM, keyPEM, f.start.Add(time.Duration(f.modVersion)*time.Second))
	f.roots.AppendCertsFromPEM(certPEM)
	return parsePEM(t, certPEM)
}

// A renewed pair is served after the next check, at most a minute later;
// in between the files are not looked at. A broken pair keeps the current
// one, logged once.
func TestCertReloader(t *testing.T) {
	f, a := newCertFixture(t, 90*24*time.Hour)
	notAfter := f.start.Add(90 * 24 * time.Hour)
	addr := certServer(t, f.r)
	if got := handshake(t, addr, f.roots); !got.Equal(a) {
		t.Fatalf("first handshake: serial %v, want A", got.SerialNumber)
	}
	if !f.r.NotAfter().Equal(notAfter) {
		t.Errorf("NotAfter %v", f.r.NotAfter())
	}

	b := f.write(t, notAfter)
	stats := f.stats.Load()
	f.clock.Add(59 * time.Second)
	if got := handshake(t, addr, f.roots); !got.Equal(a) {
		t.Error("B served before the check")
	}
	if n := f.stats.Load() - stats; n != 0 {
		t.Errorf("%d stats within the minute", n)
	}
	f.clock.Add(2 * time.Second)
	if got := handshake(t, addr, f.roots); !got.Equal(b) {
		t.Error("B not served after the check")
	}
	if n := f.stats.Load() - stats; n != 2 {
		t.Errorf("%d stats at the check, want 2", n)
	}
	if !strings.Contains(f.logs.String(), `level=INFO msg="smtp certificate reloaded"`) {
		t.Errorf("reload not logged:\n%s", f.logs)
	}

	// A half-written renewal: the old pair stays, the error is logged once
	// however often the check finds it.
	if err := os.WriteFile(f.cert, []byte("-----BEGIN CERTIFICATE-----\nhalf"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(f.cert, f.start.Add(time.Hour), f.start.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		f.clock.Add(61 * time.Second)
		if got := handshake(t, addr, f.roots); !got.Equal(b) {
			t.Error("the broken pair replaced B")
		}
	}
	if n := strings.Count(f.logs.String(), `msg="smtp certificate not reloaded, keeping the current one"`); n != 1 {
		t.Errorf("%d errors for one broken pair:\n%s", n, f.logs)
	}
	// Completed, it is loaded.
	c := f.write(t, notAfter)
	f.clock.Add(61 * time.Second)
	if got := handshake(t, addr, f.roots); !got.Equal(c) {
		t.Error("the completed renewal is not served")
	}
	if strings.Contains(f.logs.String(), "level=WARN") {
		t.Errorf("warnings:\n%s", f.logs)
	}
}

// Handshakes run while the files change and reloads happen: run with -race.
func TestCertReloaderConcurrent(t *testing.T) {
	f, _ := newCertFixture(t, 90*24*time.Hour)
	// All pairs are trusted before the first handshake: the pool is not
	// safe to change while clients read it.
	type pair struct{ cert, key []byte }
	var pairs []pair
	for range 5 {
		c, k := testCert(t, f.start.Add(-24*time.Hour), f.start.Add(90*24*time.Hour))
		f.roots.AppendCertsFromPEM(c)
		pairs = append(pairs, pair{c, k})
	}
	addr := certServer(t, f.r)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 5 {
				if handshake(t, addr, f.roots) == nil {
					return
				}
			}
		})
	}
	for i, p := range pairs {
		writePair(t, f.dir, p.cert, p.key, f.start.Add(time.Duration(i+10)*time.Second))
		f.clock.Add(61 * time.Second)
		_, _ = f.r.GetCertificate(nil)
	}
	wg.Wait()
	if got := handshake(t, addr, f.roots); got == nil || !got.Equal(parsePEM(t, pairs[len(pairs)-1].cert)) {
		t.Error("the last pair is not served")
	}
}

// Below 14 days a warning at start and then at most once a day; an expired
// certificate logs an error.
func TestCertExpiry(t *testing.T) {
	f, _ := newCertFixture(t, 10*24*time.Hour)
	warnings := func() int { return strings.Count(f.logs.String(), `level=WARN msg="smtp certificate expires soon"`) }
	if n := warnings(); n != 1 || !strings.Contains(f.logs.String(), "days_left=10") {
		t.Fatalf("%d warnings at start:\n%s", n, f.logs)
	}
	for range 3 {
		f.clock.Add(61 * time.Second)
		_, _ = f.r.GetCertificate(nil)
	}
	if n := warnings(); n != 1 {
		t.Errorf("%d warnings within the day", n)
	}
	f.clock.Add(24 * time.Hour)
	_, _ = f.r.GetCertificate(nil)
	if n := warnings(); n != 2 {
		t.Errorf("%d warnings after a day", n)
	}
	f.clock.Add(10 * 24 * time.Hour)
	_, _ = f.r.GetCertificate(nil)
	if !strings.Contains(f.logs.String(), `level=ERROR msg="smtp certificate has expired`) {
		t.Errorf("expiry not logged:\n%s", f.logs)
	}

	// 14 days or more: nothing.
	g, _ := newCertFixture(t, 14*24*time.Hour)
	if strings.Contains(g.logs.String(), "level=WARN") {
		t.Errorf("warning at 14 days:\n%s", g.logs)
	}
}

// The first load must work, and a key that every user can read is warned
// about.
func TestCertReloaderStart(t *testing.T) {
	dir := t.TempDir()
	log := slog.New(slog.NewTextHandler(&syncBuffer{}, nil))
	if _, err := NewCertReloader(filepath.Join(dir, "missing.pem"), filepath.Join(dir, "missing.key"), log); err == nil {
		t.Error("loaded missing files")
	}
	certPEM, keyPEM := testCert(t, time.Now().Add(-time.Hour), time.Now().Add(90*24*time.Hour))
	_, otherKey := testCert(t, time.Now().Add(-time.Hour), time.Now().Add(90*24*time.Hour))
	cert, key := writePair(t, dir, certPEM, otherKey, time.Now())
	if _, err := NewCertReloader(cert, key, log); err == nil {
		t.Error("loaded a key that does not match")
	}
	cert, key = writePair(t, dir, certPEM, keyPEM, time.Now())
	if err := os.Chmod(key, 0o644); err != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	if _, err := NewCertReloader(cert, key, slog.New(slog.NewTextHandler(logs, nil))); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), `level=WARN msg="smtp TLS key is readable by every user`) {
		t.Errorf("no warning for a readable key:\n%s", logs)
	}
}
