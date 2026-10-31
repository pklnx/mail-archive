package attachtext

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/tetratelabs/wazero/sys"

	"github.com/pklnx/mail-archive/internal/attachtext/pdftest"
	"github.com/pklnx/mail-archive/internal/testwait"
)

// shared is the Extractor of the whole test binary: the module is compiled
// once. Tests that set hooks restore them through setHooks.
var shared = New(nil)

// setHooks installs PDF hooks for one test.
func setHooks(t *testing.T, h pdfHooks) {
	t.Helper()
	shared.pdf.hooks = h
	t.Cleanup(func() { shared.pdf.hooks = pdfHooks{} })
}

func extractPDF(t *testing.T, data []byte) Result {
	t.Helper()
	res, err := shared.PDF(context.Background(), data)
	if err != nil {
		t.Fatalf("PDF: %v", err)
	}
	return res
}

// wantStatus fails unless res has one of the statuses.
func wantStatus(t *testing.T, name string, res Result, want ...Status) {
	t.Helper()
	for _, w := range want {
		if res.Status == w {
			return
		}
	}
	t.Errorf("%s: status %q, want one of %q", name, res.Status, want)
}

// healthy checks that the engine still extracts a regular document.
func healthy(t *testing.T) {
	t.Helper()
	if res := extractPDF(t, pdftest.Text("still healthy")); res.Status != StatusOK || res.Text != "still healthy" {
		t.Fatalf("next document: %+v", res)
	}
}

// deadlineCtx is a context whose deadline passes when expire is called, so
// a test can let the deadline pass at a moment of its choice.
type deadlineCtx struct {
	context.Context
	once sync.Once
	done chan struct{}
}

func newDeadlineCtx() *deadlineCtx {
	return &deadlineCtx{Context: context.Background(), done: make(chan struct{})}
}

func (c *deadlineCtx) expire()                     { c.once.Do(func() { close(c.done) }) }
func (c *deadlineCtx) Done() <-chan struct{}       { return c.done }
func (c *deadlineCtx) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *deadlineCtx) Err() error {
	select {
	case <-c.done:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func readFixture(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPDFText(t *testing.T) {
	res := extractPDF(t, pdftest.Text("Rechnung Müller", "Seite zwei"))
	if res.Status != StatusOK || res.Text != "Rechnung Müller\nSeite zwei" {
		t.Errorf("got %+v", res)
	}
}

// The committed fixture was made by LibreOffice with an embedded subset of
// DejaVu Sans (testdata/README.md): umlauts, the euro sign and the "ﬁ"
// ligature come through its ToUnicode map.
func TestPDFRealWorldFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/invoice.pdf")
	if err != nil {
		t.Fatal(err)
	}
	res := extractPDF(t, data)
	want := "Rechnung Nr. 2026-10\nMüller & Söhne GmbH, Straße 5, 80331 München\nGesamtbetrag: 1.234,56 €\nLeistung: Profil-Prüfung, Größe XL"
	if res.Status != StatusOK || strings.TrimSpace(res.Text) != want {
		t.Errorf("got %q (%s)", res.Text, res.Status)
	}
	if strings.ContainsRune(res.Text, 'ﬁ') {
		t.Error("ligature not normalised")
	}
}

func TestNormalizePDFText(t *testing.T) {
	if got := normalizePDFText("Pro\ufb01l \ufb00 \uff21\r\nnext\x00"); got != "Profil ff A\nnext" {
		t.Errorf("got %q", got)
	}
}

func TestPDFDamaged(t *testing.T) {
	valid := pdftest.Text("first page", "second page")
	cases := map[string]struct {
		data []byte
		want []Status
	}{
		"truncated":     {pdftest.Truncated(valid), []Status{StatusFailed, StatusOK}},
		"empty":         {nil, []Status{StatusFailed}},
		"bare header":   {[]byte("%PDF-"), []Status{StatusFailed}},
		"random":        {append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte{0x9c, 0x01, 0xff, 0x37}, 4096)...), []Status{StatusFailed}},
		"xref past end": {pdftest.XrefPastEnd(), []Status{StatusFailed, StatusOK}},
		"page cycle":    {pdftest.CyclicPages(), []Status{StatusFailed, StatusOK}},
		"nested arrays": {pdftest.NestedArrays(100_000), []Status{StatusFailed, StatusOK}},
	}
	for name, c := range cases {
		res := extractPDF(t, c.data)
		wantStatus(t, name, res, c.want...)
		if res.Status == StatusFailed && res.Text != "" {
			t.Errorf("%s: failed with text %q", name, res.Text)
		}
	}
	healthy(t)
}

// A content stream that inflates to 1 GiB stops at the memory limit. The
// linear memory lives in the mapping, not in the Go heap.
func TestPDFDecompressionBomb(t *testing.T) {
	bomb := pdftest.Bomb(1 << 30)
	if len(bomb) > 2<<20 {
		t.Fatalf("bomb is %d bytes", len(bomb))
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	res := extractPDF(t, bomb)
	peak := shared.pdf.peak.Load()
	runtime.GC()
	runtime.ReadMemStats(&after)
	// Under the race detector the inflate can take longer than the PDF
	// timeout; that ends the document as well.
	wantStatus(t, "bomb", res, StatusFailed, StatusTruncated, StatusTimeout)
	if peak > pdfMemoryLimit || peak == 0 {
		t.Errorf("linear memory peak %d, limit %d", peak, pdfMemoryLimit)
	}
	if grow := int64(after.HeapAlloc) - int64(before.HeapAlloc); grow > 64<<20 {
		t.Errorf("Go heap grew by %d bytes", grow)
	}
	healthy(t)
}

func TestPDFPageLimit(t *testing.T) {
	res := extractPDF(t, pdftest.Pages(10_000, func(int) string { return "page" }))
	if res.Status != StatusTruncated || strings.Count(res.Text, "page") != maxPages {
		t.Errorf("status %s, %d pages read", res.Status, strings.Count(res.Text, "page"))
	}
}

func TestPDFTextLimit(t *testing.T) {
	line := strings.Repeat("ä", 50) + " Grüße"
	page := strings.TrimSuffix(strings.Repeat(line+"\n", 1500), "\n") // about 160 KiB per page
	res := extractPDF(t, pdftest.Text(page, page, page))
	if res.Status != StatusTruncated || len(res.Text) > MaxText || len(res.Text) < MaxText-8 {
		t.Errorf("status %s, %d bytes", res.Status, len(res.Text))
	}
	if !utf8.ValidString(res.Text) {
		t.Error("cut inside a rune")
	}
}

func TestPDFPasswords(t *testing.T) {
	// Fixtures with AES-256 (R6) encryption, see testdata/README.md.
	res := extractPDF(t, readFixture(t, "testdata/encrypted-user.pdf"))
	if res.Status != StatusEncrypted || res.Text != "" {
		t.Errorf("user password: %+v", res)
	}
	res = extractPDF(t, readFixture(t, "testdata/encrypted-owner.pdf"))
	if res.Status != StatusOK || res.Text != "open words" {
		t.Errorf("owner password only: %+v", res)
	}
}

func TestPDFTooLarge(t *testing.T) {
	big := pdftest.Large(MaxPDFSize+1, "too large")
	if res := extractPDF(t, big); res.Status != StatusTooLarge {
		t.Errorf("25 MiB + 1: %s", res.Status)
	}
	// The largest allowed PDF extracts within the memory limit: PDFium
	// reads it through the callback, so the file is not copied into
	// linear memory.
	res := extractPDF(t, pdftest.Large(MaxPDFSize, "exactly the limit"))
	if res.Status != StatusOK || res.Text != "exactly the limit" {
		t.Errorf("25 MiB: %+v", res)
	}
	if peak := shared.pdf.peak.Load(); peak > pdfMemoryLimit {
		t.Errorf("linear memory peak %d", peak)
	}
}

func TestPDFTimeout(t *testing.T) {
	// A deadline that passes while WebAssembly code runs.
	ctx := newDeadlineCtx()
	setHooks(t, pdfHooks{start: ctx.expire})
	res, err := shared.PDF(ctx, pdftest.Text("slow"))
	if err != nil || res.Status != StatusTimeout || res.Text != "" {
		t.Fatalf("got %+v, %v", res, err)
	}
	shared.pdf.hooks = pdfHooks{}
	// A deadline that has already passed.
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	res, err = shared.PDF(expired, pdftest.Text("late"))
	if err != nil || res.Status != StatusTimeout {
		t.Fatalf("expired deadline: %+v, %v", res, err)
	}
	healthy(t)
}

// Killing an instance while its WebAssembly code runs closes it while the
// call is still running. Its memory is given back (Free) exactly once and
// unmapped only after the call has returned. (Free runs inside Kill, or
// when the call notices the close, depending on which of wazero's two
// close paths wins.) The process survives, and the next document works.
func TestPDFKillDuringExecution(t *testing.T) {
	var (
		mu     sync.Mutex
		events []string
		killed testwait.Signal
	)
	record := func(e string) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	}
	alloc := shared.pdf.alloc
	alloc.hooks = allocHooks{
		free:  func() { record("free") },
		unmap: func() { record("unmap") },
	}
	t.Cleanup(func() { alloc.hooks = allocHooks{} })
	for i := range 20 {
		mu.Lock()
		events = nil
		mu.Unlock()
		ctx := newDeadlineCtx()
		setHooks(t, pdfHooks{
			start: func() {
				wait := killed.C()
				ctx.expire()
				// Still inside the WebAssembly call (this is its file
				// read callback): wait until Kill has returned.
				select {
				case <-wait:
				case <-time.After(testwait.Limit(t)):
					t.Error("Kill did not return")
				}
				record("call running")
			},
			killed: func() { record("killed"); killed.Notify() },
		})
		res, err := shared.PDF(ctx, pdftest.Text("killed"))
		if err != nil || res.Status != StatusTimeout {
			t.Fatalf("round %d: %+v, %v", i, res, err)
		}
		mu.Lock()
		got := strings.Join(events, ",")
		mu.Unlock()
		if got != "free,killed,call running,unmap" && got != "killed,call running,free,unmap" {
			t.Fatalf("round %d: events %s", i, got)
		}
	}
	shared.pdf.hooks = pdfHooks{}
	healthy(t)
}

// Cancelling the caller's context (shutdown) kills the instance too, not
// only the PDF's own timer, and is reported as an error, not a status.
func TestPDFShutdownKills(t *testing.T) {
	var freed int
	alloc := shared.pdf.alloc
	alloc.hooks = allocHooks{free: func() { freed++ }}
	t.Cleanup(func() { alloc.hooks = allocHooks{} })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	setHooks(t, pdfHooks{start: cancel})
	res, err := shared.PDF(ctx, pdftest.Text("shutdown"))
	if !errors.Is(err, context.Canceled) || res.Status != "" {
		t.Fatalf("got %+v, %v", res, err)
	}
	if freed != 1 {
		t.Errorf("memory freed %d times", freed)
	}
	shared.pdf.hooks = pdfHooks{}
	healthy(t)
}

func TestPDFPanicRecovered(t *testing.T) {
	setHooks(t, pdfHooks{load: func() { panic("injected") }})
	if res := extractPDF(t, pdftest.Text("panic")); res.Status != StatusFailed {
		t.Fatalf("status %s", res.Status)
	}
	shared.pdf.hooks = pdfHooks{}
	healthy(t)
}

func TestPDFMmapFailure(t *testing.T) {
	alloc := shared.pdf.alloc
	orig := alloc.mmap
	alloc.mmap = func(int) ([]byte, error) { return nil, errors.New("injected: cannot allocate memory") }
	t.Cleanup(func() { alloc.mmap = orig })
	if res := extractPDF(t, pdftest.Text("no memory")); res.Status != StatusFailed {
		t.Fatalf("status %s", res.Status)
	}
	alloc.mmap = orig
	healthy(t)
}

// The module has no file system: opening the path of an existing host
// PDF fails.
func TestPDFSandboxHasNoFileSystem(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host.pdf")
	if err := os.WriteFile(path, pdftest.Text("host file"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := shared.pdf
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.compile(); err != nil {
		t.Fatal(err)
	}
	var loadErr error
	err := e.run(context.Background(), func(inst pdfium.Pdfium) error {
		_, loadErr = inst.FPDF_LoadDocument(&requests.FPDF_LoadDocument{Path: &path})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if loadErr == nil {
		t.Fatal("a host file opened inside the sandbox")
	}
}

// Text handed out is Go memory: it stays readable after the instance and
// its mapping are gone.
func TestPDFTextOutlivesInstance(t *testing.T) {
	res := extractPDF(t, pdftest.Text("kept after unmap"))
	runtime.GC()
	if c := strings.Clone(res.Text); c != "kept after unmap" {
		t.Errorf("text %q", c)
	}
}

// A kill interrupts WebAssembly code that runs outside go-pdfium's
// callbacks (#423): the call that runs when the kill lands ends with a
// sys.ExitError, which only WithCloseOnContextDone(true) gives. The kill is
// sent while the module grows its memory (inflating a Flate bomb while it
// loads a page), the only point inside pure WebAssembly code that a test
// can observe; the grow waits until Kill has returned, so that it does not
// race wazero's close (see pdfEngine), and the module then notices the
// close in its own code.
func TestPDFKillInterruptsRunningCode(t *testing.T) {
	e := shared.pdf
	takePDFSlot()
	defer pdfSlot.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.compile(); err != nil {
		t.Fatal(err)
	}
	data := pdftest.Bomb(1 << 30)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var (
		armed  atomic.Bool
		killed testwait.Signal
		kills  atomic.Int32
	)
	e.hooks = pdfHooks{killed: func() { kills.Add(1); killed.Notify() }}
	alloc := e.alloc
	alloc.hooks = allocHooks{grow: func() {
		if !armed.CompareAndSwap(true, false) {
			return
		}
		wait := killed.C()
		cancel()
		select {
		case <-wait:
		case <-time.After(testwait.Limit(t)):
			t.Error("Kill did not return")
		}
	}}
	t.Cleanup(func() { alloc.hooks = allocHooks{}; e.hooks = pdfHooks{} })
	var pageErr error
	err := e.run(ctx, func(inst pdfium.Pdfium) error {
		doc, err := inst.FPDF_LoadCustomDocument(&requests.FPDF_LoadCustomDocument{Reader: bytes.NewReader(data), Size: int64(len(data))})
		if err != nil {
			return err
		}
		armed.Store(true)
		page, err := inst.FPDF_LoadPage(&requests.FPDF_LoadPage{Document: doc.Document, Index: 0})
		if err == nil {
			_, err = inst.FPDFText_LoadPage(&requests.FPDFText_LoadPage{Page: requests.Page{ByReference: &page.Page}})
		}
		pageErr = err
		return err
	})
	if !errors.Is(err, errKilled) || kills.Load() != 1 {
		t.Fatalf("run: %v, %d kills", err, kills.Load())
	}
	if armed.Load() {
		t.Fatal("the page was loaded without growing the memory")
	}
	var exit *sys.ExitError
	if !errors.As(pageErr, &exit) {
		t.Fatalf("the running call ended with %v (%T), want a sys.ExitError", pageErr, pageErr)
	}
}

// The garbage of the one-time compile (about 250 MiB) is collected and
// given back to the system right after it, not left for the next GC and
// the scavenger (#416's gate: idle RSS after the compile).
func TestPDFCompileGarbageReturned(t *testing.T) {
	e := New(nil)
	t.Cleanup(func() { _ = e.Close() })
	if res, err := e.PDF(context.Background(), pdftest.Text("a")); err != nil || res.Status != StatusOK {
		t.Fatalf("%+v, %v", res, err)
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	if held := ms.HeapSys - ms.HeapReleased; held > 128<<20 {
		t.Errorf("the Go heap holds %d MiB after the compile", held>>20)
	}
}
