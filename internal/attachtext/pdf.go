package attachtext

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/klippa-app/go-pdfium"
	pdfiumerrors "github.com/klippa-app/go-pdfium/errors"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/responses"
	"github.com/klippa-app/go-pdfium/webassembly"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
	"golang.org/x/text/unicode/norm"
)

// pdfEngine runs PDFium compiled to WebAssembly in wazero, one document at
// a time, each in a new module instance that is thrown away afterwards.
//
// The sandbox: no file system (an empty FSConfig replaces go-pdfium's
// default, which mounts "/"), no environment variables, stdout and stderr
// discarded, and no network (WASI preview 1 has no sockets without
// preopened ones). Linear memory is limited to pdfMemoryLimit and backed by
// mmapAllocator. A timeout or a cancelled context kills the instance
// through Kill, which interrupts running WebAssembly code
// (WithCloseOnContextDone): go-pdfium runs every call under the worker's
// own context, so the context of a call interrupts nothing.
//
// A Kill that lands while the module grows its memory is a data race inside
// wazero (ensureResourcesClosed against MemoryInstance.Grow): harmless, as
// the instance is thrown away, but reported by -race. The tests therefore
// kill on the file callback (hooks.start), or hold the grow until the kill
// has returned (TestPDFKillInterruptsRunningCode).
//
// Only one PDF is read at a time in the whole process (pdfSlot), whatever
// the number of Extractors and job workers (maintainer decision 12 in
// #390): go-pdfium keeps the readers of open documents in a process-wide
// table keyed by an index that the WebAssembly code reads from its own
// memory, so a PDF exploiting a PDFium memory bug could otherwise read
// another document that is open at the same time.
type pdfEngine struct {
	mu   sync.Mutex // the fields below
	pool pdfium.Pool
	// err is the compile error, kept for the process: compiling again
	// would fail again. It is logged once.
	err       error
	errLogged bool
	alloc     *mmapAllocator
	logger    *slog.Logger
	hooks     pdfHooks
	// limit overrides pdfMemoryLimit and memDoc opens documents with
	// FPDF_LoadMemDocument64; both only for the measurements.
	limit  uint64
	memDoc bool
	// peak is the largest linear memory of the last document, in bytes.
	peak atomic.Uint64
}

// pdfHooks are test seams; nil hooks are skipped.
type pdfHooks struct {
	// start runs when the module first reads the document, that is while
	// WebAssembly code runs (FPDF_LoadCustomDocument calls back into Go).
	start func()
	// load runs before the document is opened; tests panic there.
	load func()
	// killed runs after Kill has returned.
	killed func()
	// compile runs before the module is compiled; an error fails it.
	compile func() error
}

var errKilled = errors.New("pdf: instance killed")

// ErrUnavailable is returned for a PDF when the WebAssembly module could not
// be compiled in this process. It stays so until the process restarts.
var ErrUnavailable = errors.New("attachment text: the PDF extractor is unavailable")

// ErrBusy is returned when the caller's deadline passed while a PDF waited
// for another one to finish: nothing was read, and the work is to be tried
// again (not a timeout of the document).
var ErrBusy = errors.New("attachment text: the deadline passed while waiting for another PDF")

// pdfSlot lets one PDF at a time be read in the process (see pdfEngine).
var pdfSlot = make(chan struct{}, 1)

// onPDFWait is a test seam: it runs when a PDF has to wait for another one.
var onPDFWait func()

// takePDFSlot waits until no other PDF is read in the process, or ctx
// ends: then it returns ErrBusy for a deadline, else ctx's error. It
// returns how long it waited. releasePDFSlot gives the slot back.
func takePDFSlot(ctx context.Context) (time.Duration, error) {
	select {
	case pdfSlot <- struct{}{}:
		return 0, nil
	default:
	}
	if onPDFWait != nil {
		onPDFWait()
	}
	start := time.Now()
	select {
	case pdfSlot <- struct{}{}:
		return time.Since(start), nil
	case <-ctx.Done():
		if isDeadline(ctx.Err()) {
			return time.Since(start), ErrBusy
		}
		return time.Since(start), ctx.Err()
	}
}

func releasePDFSlot() { <-pdfSlot }

// compile compiles the module once per process. There is no compilation
// cache on disk, so no executable code is written to the data directory.
func (e *pdfEngine) compile() error {
	if e.pool != nil || e.err != nil {
		return e.err
	}
	if e.hooks.compile != nil {
		if err := e.hooks.compile(); err != nil {
			e.err = fmt.Errorf("pdf: compile: %w", err)
			return e.err
		}
	}
	// A custom RuntimeConfig must keep CoreFeaturesV2 together with
	// exception handling: the bundled module lowers setjmp/longjmp to
	// WebAssembly exceptions (go-pdfium's Init says the same).
	rc := wazero.NewRuntimeConfig().
		WithCoreFeatures(api.CoreFeaturesV2 | experimental.CoreFeaturesExceptionHandling).
		WithMemoryLimitPages(uint32(e.memoryLimit() / 65536)). //nolint:gosec // at most 4096 pages
		WithCloseOnContextDone(true)
	// wazero reads the allocator from the context the instances are
	// created with, which go-pdfium derives from Config.Context.
	ctx := experimental.WithMemoryAllocator(context.Background(), e.alloc.memoryAllocator())
	pool, err := webassembly.Init(webassembly.Config{
		Context: ctx,
		MinIdle: 0,
		MaxIdle: 1,
		// Unbounded: pdfSlot already runs one document at a time, and a
		// creation that panics (a failed mmap) never returns its count.
		MaxTotal:      -1,
		FSConfig:      wazero.NewFSConfig(),
		RuntimeConfig: rc,
		Stdout:        io.Discard,
		Stderr:        io.Discard,
		ReuseWorkers:  false,
	})
	if err != nil {
		e.err = fmt.Errorf("pdf: compile: %w", err)
		return e.err
	}
	e.pool = pool
	// The compile leaves about 250 MiB of garbage in the Go heap, which an
	// idle worker would otherwise keep for minutes: give it back once, so
	// that the process returns to its idle budget (96 MiB, see
	// docs/guide/operations.md, "Hardware and budgets").
	debug.FreeOSMemory()
	return nil
}

func (e *pdfEngine) memoryLimit() uint64 {
	if e.limit != 0 {
		return e.limit
	}
	return pdfMemoryLimit
}

// Warm compiles the module if it is not compiled yet.
func (e *pdfEngine) warm() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.compile()
}

// run gives fn a new instance and discards it afterwards. When ctx ends
// first, the instance is killed and run returns errKilled; fn's result is
// to be dropped then. A panic in fn or while the instance is created is
// returned as an error.
// The caller holds e.mu and has compiled the module.
func (e *pdfEngine) run(ctx context.Context, fn func(pdfium.Pdfium) error) (err error) {
	// Unmap only after the instance is gone and Kill has returned (see
	// mmapAllocator), so this deferred call runs last.
	defer func() {
		e.alloc.release()
		e.peak.Store(e.alloc.peakAndReset())
	}()
	inst, err := e.instance(ctx)
	if err != nil {
		return err
	}
	done := make(chan struct{})
	watched := make(chan struct{})
	var killed atomic.Bool
	go func() {
		defer close(watched)
		select {
		case <-ctx.Done():
			killed.Store(true)
			_ = inst.Kill()
			if e.hooks.killed != nil {
				e.hooks.killed()
			}
		case <-done:
		}
	}()
	err = protect(func() error { return fn(inst) })
	close(done)
	<-watched
	if killed.Load() {
		return errKilled
	}
	_ = inst.Close()
	return err
}

// instance creates a module instance. A failed mmap makes wazero panic
// while it creates the memory; that becomes an error here.
func (e *pdfEngine) instance(ctx context.Context) (inst pdfium.Pdfium, err error) {
	err = protect(func() error {
		var err error
		inst, err = e.pool.GetInstanceWithContext(ctx)
		return err
	})
	if err != nil {
		e.logger.Warn("attachment text: cannot create a PDF instance", "err", err)
		return nil, fmt.Errorf("pdf: instance: %w", err)
	}
	return inst, nil
}

// protect runs fn and turns a panic into an error.
func protect(fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("pdf: panic: %v", r)
		}
	}()
	return fn()
}

// pdfText is the text of one PDF and how it ended.
type pdfText struct {
	text   string
	status Status
}

// extract returns the text of a PDF of at most maxText bytes from at most
// maxPages pages. It returns an error only when ctx was cancelled (not
// when its deadline passed: that is StatusTimeout).
//
// When the module cannot be compiled, it returns ErrUnavailable, and logs
// the cause once per engine.
//
// The PDF has min(pdfTimeout, budget) once it is its turn; the time it
// waited for its turn, which it returns, is not counted.
func (e *pdfEngine) extract(ctx context.Context, data []byte, maxText int, budget time.Duration) (pdfText, time.Duration, error) {
	waited, err := takePDFSlot(ctx)
	if err != nil {
		return pdfText{}, waited, err
	}
	defer releasePDFSlot()
	e.mu.Lock()
	defer e.mu.Unlock()
	// The one-time compile does not count against the document's time.
	if err := e.compile(); err != nil {
		if !e.errLogged {
			e.errLogged = true
			e.logger.Error("attachment text: cannot compile the PDF module; PDFs are not extracted until a restart", "err", err)
		}
		return pdfText{}, waited, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if ctx.Err() != nil && !isDeadline(ctx.Err()) {
		return pdfText{}, waited, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(ctx, min(pdfTimeout, budget))
	defer cancel()
	var out pdfText
	reader := &docReader{r: bytes.NewReader(data), start: e.hooks.start}
	defer reader.drop()
	err = e.run(ctx, func(inst pdfium.Pdfium) error {
		var err error
		out, err = e.read(ctx, inst, reader, int64(len(data)), maxText)
		return err
	})
	switch {
	case ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded):
		return pdfText{}, waited, ctx.Err()
	case ctx.Err() != nil || errors.Is(err, errKilled):
		return pdfText{status: StatusTimeout}, waited, nil
	case err != nil:
		return pdfText{status: StatusFailed}, waited, nil
	}
	return out, waited, nil
}

// read opens the document and reads its text page by page.
func (e *pdfEngine) read(ctx context.Context, inst pdfium.Pdfium, r io.ReadSeeker, size int64, maxText int) (pdfText, error) {
	if e.hooks.load != nil {
		e.hooks.load()
	}
	// No password: a PDF with only an owner password opens with the empty
	// user password; one that needs a user password fails with ErrPassword.
	// The document is read through a callback, block by block, so it is
	// not copied into linear memory.
	var doc struct{ Document references.FPDF_DOCUMENT }
	var err error
	if e.memDoc {
		var data []byte
		if data, err = io.ReadAll(r); err == nil {
			var res *responses.FPDF_LoadMemDocument64
			if res, err = inst.FPDF_LoadMemDocument64(&requests.FPDF_LoadMemDocument64{Data: &data}); err == nil {
				doc.Document = res.Document
			}
		}
	} else {
		var res *responses.FPDF_LoadCustomDocument
		if res, err = inst.FPDF_LoadCustomDocument(&requests.FPDF_LoadCustomDocument{Reader: r, Size: size}); err == nil {
			doc.Document = res.Document
		}
	}
	if errors.Is(err, pdfiumerrors.ErrPassword) {
		return pdfText{status: StatusEncrypted}, nil
	}
	if err != nil {
		return pdfText{}, err
	}
	defer func() {
		_, _ = inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document})
	}()
	count, err := inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
	if err != nil {
		return pdfText{}, err
	}
	if count.PageCount <= 0 {
		return pdfText{}, errors.New("pdf: no pages")
	}
	var b strings.Builder
	truncated := count.PageCount > maxPages
	read := 0
	for i := range min(count.PageCount, maxPages) {
		if ctx.Err() != nil {
			return pdfText{}, ctx.Err()
		}
		left := maxText - b.Len()
		if left < 0 {
			truncated = true
			break
		}
		text, err := pageText(inst, doc.Document, i, left+1)
		if err != nil {
			continue // a broken page; the others may still have text
		}
		read++
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(text)
	}
	if read == 0 {
		return pdfText{}, errors.New("pdf: no readable page")
	}
	text := normalizePDFText(b.String())
	if len(text) > maxText {
		text, truncated = truncateText(text, maxText), true
	}
	switch {
	case strings.TrimSpace(text) == "":
		return pdfText{status: StatusNoText}, nil
	case truncated:
		return pdfText{text: text, status: StatusTruncated}, nil
	}
	return pdfText{text: text, status: StatusOK}, nil
}

// pageText returns at most maxChars UTF-16 code units of page i's text.
func pageText(inst pdfium.Pdfium, doc references.FPDF_DOCUMENT, i, maxChars int) (string, error) {
	page, err := inst.FPDF_LoadPage(&requests.FPDF_LoadPage{Document: doc, Index: i})
	if err != nil {
		return "", err
	}
	defer func() { _, _ = inst.FPDF_ClosePage(&requests.FPDF_ClosePage{Page: page.Page}) }()
	tp, err := inst.FPDFText_LoadPage(&requests.FPDFText_LoadPage{Page: requests.Page{ByReference: &page.Page}})
	if err != nil {
		return "", err
	}
	defer func() { _, _ = inst.FPDFText_ClosePage(&requests.FPDFText_ClosePage{TextPage: tp.TextPage}) }()
	n, err := inst.FPDFText_CountChars(&requests.FPDFText_CountChars{TextPage: tp.TextPage})
	if err != nil {
		return "", err
	}
	if n.Count <= 0 {
		return "", nil
	}
	t, err := inst.FPDFText_GetText(&requests.FPDFText_GetText{TextPage: tp.TextPage, StartIndex: 0, Count: min(n.Count, maxChars)})
	if err != nil {
		return "", err
	}
	return t.Text, nil
}

// docReader is the document as PDFium reads it through its file access
// callback. go-pdfium keeps the reader in a process-wide table that a
// killed instance never clears, so drop cuts the reference to the bytes
// once the document is done; the table then holds only this small struct.
type docReader struct {
	mu      sync.Mutex
	r       *bytes.Reader
	start   func()
	started bool
}

var errDropped = errors.New("pdf: document dropped")

func (d *docReader) Read(p []byte) (int, error) {
	d.mu.Lock()
	r, start := d.r, d.start
	first := !d.started
	d.started = true
	d.mu.Unlock()
	if first && start != nil {
		start()
	}
	if r == nil {
		return 0, errDropped
	}
	return r.Read(p)
}

func (d *docReader) Seek(off int64, whence int) (int64, error) {
	d.mu.Lock()
	r := d.r
	d.mu.Unlock()
	if r == nil {
		return 0, errDropped
	}
	return r.Seek(off, whence)
}

func (d *docReader) drop() {
	d.mu.Lock()
	d.r = nil
	d.mu.Unlock()
}

// normalizePDFText turns PDFium's line ends into "\n" and normalises to
// NFKC, so compatibility characters such as the ligatures "ﬁ" and "ﬀ" or
// full-width letters become what a search types. (PDFium already expands
// some ligatures itself, not all.)
func normalizePDFText(s string) string {
	return cleanText(norm.NFKC.String(strings.ReplaceAll(s, "\r\n", "\n")))
}
