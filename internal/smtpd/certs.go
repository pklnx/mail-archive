package smtpd

import (
	"crypto/tls"
	"crypto/x509"
	"io/fs"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Checking and warning intervals of a CertReloader.
const (
	// certCheckEvery is how often a handshake looks at the files at most.
	certCheckEvery = time.Minute
	// certExpiryWarning is how long before NotAfter the warnings start.
	certExpiryWarning = 14 * 24 * time.Hour
	// certWarnEvery is how often an expiry warning is repeated at most.
	certWarnEvery = 24 * time.Hour
)

// CertReloader serves the certificate and key of two PEM files, and loads
// them again when they change: a renewal (certbot or lego with a deploy
// hook that copies the files) is picked up within a minute, without a
// signal or a restart. A pair that does not load (a half-written renewal,
// a key that does not match) keeps the current one; it is logged once and
// tried again at the next check. Use GetCertificate in a tls.Config.
type CertReloader struct {
	certFile, keyFile string
	log               *slog.Logger
	now               func() time.Time
	stat              func(string) (fs.FileInfo, error)

	// cur is the pair handshakes get, read without the lock.
	cur atomic.Pointer[loadedCert]

	mu        sync.Mutex // serialises checks and reloads
	checked   time.Time
	loaded    [2]fileStamp // of the files cur came from
	failed    [2]fileStamp // of the files that failed last, logged once
	statError bool         // a stat error is logged
	warned    time.Time    // last expiry warning or error
}

type loadedCert struct {
	cert     *tls.Certificate
	notAfter time.Time
}

// fileStamp tells whether a file changed.
type fileStamp struct {
	mod  time.Time
	size int64
}

// NewCertReloader loads the pair; it fails if that fails. It warns at once
// if the certificate expires within 14 days.
func NewCertReloader(certFile, keyFile string, log *slog.Logger) (*CertReloader, error) {
	return newCertReloader(certFile, keyFile, log, time.Now, os.Stat)
}

func newCertReloader(certFile, keyFile string, log *slog.Logger, now func() time.Time, stat func(string) (fs.FileInfo, error)) (*CertReloader, error) {
	r := &CertReloader{certFile: certFile, keyFile: keyFile, log: log, now: now, stat: stat}
	r.mu.Lock()
	defer r.mu.Unlock()
	st, key, err := r.stamps()
	if err != nil {
		return nil, err
	}
	if err := r.load(st, key); err != nil {
		return nil, err
	}
	r.checked = now()
	r.checkExpiry(r.checked)
	return r, nil
}

// GetCertificate is tls.Config.GetCertificate.
func (r *CertReloader) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.check()
	return r.cur.Load().cert, nil
}

// NotAfter is when the current certificate expires.
func (r *CertReloader) NotAfter() time.Time { return r.cur.Load().notAfter }

// check looks at the files at most once per certCheckEvery, reloads them
// when they changed, and warns about expiry.
func (r *CertReloader) check() {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if now.Sub(r.checked) < certCheckEvery {
		return
	}
	r.checked = now
	st, key, err := r.stamps()
	switch {
	case err != nil:
		if !r.statError {
			r.statError = true
			r.log.Error("smtp certificate not checked, keeping the current one", "err", err)
		}
	case st != r.loaded:
		r.statError = false
		if err := r.load(st, key); err != nil {
			if st != r.failed {
				r.failed = st
				r.log.Error("smtp certificate not reloaded, keeping the current one", "cert", r.certFile, "key", r.keyFile, "err", err)
			}
			break
		}
		r.failed = [2]fileStamp{}
		r.warned = time.Time{} // a new certificate warns at once
		r.log.Info("smtp certificate reloaded", "not_after", r.NotAfter())
	default:
		r.statError = false
	}
	r.checkExpiry(now)
}

// stamps stats both files. mu is held.
func (r *CertReloader) stamps() ([2]fileStamp, fs.FileInfo, error) {
	c, err := r.stat(r.certFile)
	if err != nil {
		return [2]fileStamp{}, nil, err
	}
	k, err := r.stat(r.keyFile)
	if err != nil {
		return [2]fileStamp{}, nil, err
	}
	return [2]fileStamp{{c.ModTime(), c.Size()}, {k.ModTime(), k.Size()}}, k, nil
}

// load reads the pair and makes it current. mu is held.
func (r *CertReloader) load(st [2]fileStamp, key fs.FileInfo) error {
	pair, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		return err
	}
	leaf := pair.Leaf
	if leaf == nil {
		if leaf, err = x509.ParseCertificate(pair.Certificate[0]); err != nil {
			return err
		}
	}
	if key.Mode().Perm()&0o004 != 0 {
		r.log.Warn("smtp TLS key is readable by every user of the machine; make it readable by the server only (mode 0600 or 0400)", "key", r.keyFile)
	}
	r.cur.Store(&loadedCert{cert: &pair, notAfter: leaf.NotAfter})
	r.loaded = st
	return nil
}

// checkExpiry warns while the certificate expires within
// certExpiryWarning, and logs an error once it has expired, each at most
// once per certWarnEvery. mu is held.
func (r *CertReloader) checkExpiry(now time.Time) {
	notAfter := r.NotAfter()
	left := notAfter.Sub(now)
	if left >= certExpiryWarning || !r.warned.IsZero() && now.Sub(r.warned) < certWarnEvery {
		return
	}
	r.warned = now
	if left <= 0 {
		r.log.Error("smtp certificate has expired: mail servers that verify it cannot log in", "not_after", notAfter)
		return
	}
	r.log.Warn("smtp certificate expires soon", "not_after", notAfter, "days_left", int(left/(24*time.Hour)))
}
