package attachtext

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/attachtext/pdftest"
)

// TestGateMeasurements prints the measurements of the gate in #390
// (revision 1, section 1, with the targets of revision 2, section 3). It
// is skipped unless ATTACHTEXT_MEASURE=1. ATTACHTEXT_CORPUS lists corpus
// directories (separated by ':'); their PDFs are found recursively.
//
// Every RSS figure comes from a fresh process (this test binary run again
// for one phase), read from /proc/self/status. A phase's peak is VmHWM
// after it minus VmRSS before it, with VmHWM reset through
// /proc/self/clear_refs first.
func TestGateMeasurements(t *testing.T) {
	if phase := os.Getenv("ATTACHTEXT_MEASURE_PHASE"); phase != "" {
		measurePhase(t, phase)
		return
	}
	if os.Getenv("ATTACHTEXT_MEASURE") != "1" {
		t.Skip("set ATTACHTEXT_MEASURE=1 to measure")
	}
	dir := t.TempDir()
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	heavy := strings.TrimSuffix(strings.Repeat("Rechnungsposition Menge Einzelpreis Gesamtpreis Mehrwertsteuer\n", 60), "\n")
	fixtures := map[string]string{
		"bomb (1 GiB Flate)": write("bomb.pdf", pdftest.Bomb(1<<30)),
		"25 MiB fixture":     write("large.pdf", pdftest.Large(MaxPDFSize, "large")),
		"500 pages, heavy":   write("pages500.pdf", pdftest.Pages(500, func(i int) string { return fmt.Sprintf("%d\n%s", i, heavy) })),
		"2 pages":            write("pages2.pdf", pdftest.Pages(2, func(i int) string { return fmt.Sprintf("%d\n%s", i, heavy) })),
		"100 pages":          write("pages100.pdf", pdftest.Pages(100, func(i int) string { return fmt.Sprintf("%d\n%s", i, heavy) })),
	}

	// Compile: default GOMAXPROCS and 1; with GOGC 20 for the compile.
	for _, env := range [][]string{nil, {"GOMAXPROCS=1"}, {"ATTACHTEXT_GCPERCENT=20"}, {"GOMAXPROCS=1", "ATTACHTEXT_GCPERCENT=20"}} {
		for range 5 {
			r := child(t, "compile", env...)
			t.Logf("| compile %v | %.2f s | peak +%.1f MiB | after +%.1f MiB |", env, r["seconds"], r["peakMiB"], r["afterMiB"])
		}
	}

	// The corpus, at 256 MiB and at the candidate limits.
	var corpus []string
	for _, d := range filepath.SplitList(os.Getenv("ATTACHTEXT_CORPUS")) {
		_ = filepath.WalkDir(d, func(p string, de fs.DirEntry, err error) error {
			if err == nil && !de.IsDir() && strings.EqualFold(filepath.Ext(p), ".pdf") {
				if fi, err := de.Info(); err == nil && fi.Size() <= MaxPDFSize {
					corpus = append(corpus, p)
				}
			}
			return nil
		})
	}
	sort.Strings(corpus)
	list := write("corpus.txt", []byte(strings.Join(corpus, "\n")))
	runs := map[int]map[string]corpusEntry{}
	for _, mib := range []int{256, 128, 96, 64} {
		out := filepath.Join(dir, fmt.Sprintf("corpus-%d.jsonl", mib))
		child(t, "corpus", "ATTACHTEXT_LIST="+list, "ATTACHTEXT_OUT="+out, fmt.Sprintf("ATTACHTEXT_LIMIT_MIB=%d", mib))
		runs[mib] = readCorpus(t, out)
	}
	base := runs[256]
	var peaks []uint64
	largest, largestSize := "", int64(0)
	statuses := map[Status]int{}
	for p, e := range base {
		peaks = append(peaks, e.Peak)
		statuses[e.Status]++
		if fi, err := os.Stat(p); err == nil && fi.Size() > largestSize {
			largest, largestSize = p, fi.Size()
		}
	}
	sort.Slice(peaks, func(i, j int) bool { return peaks[i] < peaks[j] })
	if len(peaks) > 0 {
		maxPeak := peaks[len(peaks)-1]
		p99 := peaks[(len(peaks)*99)/100]
		t.Logf("| corpus | %d PDFs | linear memory max %.1f MiB, p99 %.1f MiB | statuses %v |", len(peaks), mib(maxPeak), mib(p99), statuses)
		var top []string
		for p, e := range base {
			if e.Peak >= maxPeak*3/4 {
				top = append(top, fmt.Sprintf("%s %.1f MiB %s", filepath.Base(p), mib(e.Peak), e.Status))
			}
		}
		sort.Strings(top)
		t.Logf("| corpus peaks ≥ 75%% of max | %s |", strings.Join(top, "; "))
	}
	for _, l := range []int{64, 96, 128} {
		diff := 0
		var names []string
		for p, e := range base {
			if o := runs[l][p]; o.Status != e.Status || o.Sum != e.Sum {
				diff++
				names = append(names, fmt.Sprintf("%s %s→%s", filepath.Base(p), e.Status, o.Status))
			}
		}
		sort.Strings(names)
		t.Logf("| limit %d MiB | %d of %d PDFs differ from 256 MiB | %s |", l, diff, len(base), strings.Join(names, "; "))
	}

	// Peak RSS per document, each in a fresh process.
	docs := map[string]string{"bomb (1 GiB Flate)": fixtures["bomb (1 GiB Flate)"], "25 MiB fixture": fixtures["25 MiB fixture"], "500 pages, heavy": fixtures["500 pages, heavy"]}
	if largest != "" {
		docs["largest corpus PDF ("+filepath.Base(largest)+")"] = largest
	}
	var maxDoc string
	var maxDocPeak uint64
	for p, e := range base {
		if e.Peak > maxDocPeak {
			maxDoc, maxDocPeak = p, e.Peak
		}
	}
	if maxDoc != "" {
		docs["corpus PDF with the largest linear memory ("+filepath.Base(maxDoc)+")"] = maxDoc
	}
	names := make([]string, 0, len(docs))
	for n := range docs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		r := child(t, "doc", "ATTACHTEXT_DOC="+docs[n])
		t.Logf("| %s | peak +%.1f MiB over the baseline (%.1f MiB) | linear %.1f MiB | %s | %.2f s |", n, r["peakMiB"], r["baselineMiB"], r["linearMiB"], Status(fmt.Sprint(r["status"])), r["seconds"])
	}

	// Extraction time, callback against copying the file.
	for _, n := range []string{"2 pages", "100 pages", "25 MiB fixture"} {
		r := child(t, "timing", "ATTACHTEXT_DOC="+fixtures[n])
		t.Logf("| time %s | LoadCustomDocument %.1f ms | LoadMemDocument64 %.1f ms |", n, r["customMs"], r["memMs"])
	}
}

type corpusEntry struct {
	Path   string  `json:"path"`
	Status Status  `json:"status"`
	Sum    string  `json:"sum"`
	Peak   uint64  `json:"peak"`
	Ms     float64 `json:"ms"`
}

func readCorpus(t *testing.T, path string) map[string]corpusEntry {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string]corpusEntry{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e corpusEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		out[e.Path] = e
	}
	return out
}

func mib(b uint64) float64 { return float64(b) / (1 << 20) }

// child runs one phase in a fresh process and returns its result.
func child(t *testing.T, phase string, env ...string) map[string]any {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestGateMeasurements$", "-test.count=1", "-test.timeout=2h")
	cmd.Env = append(os.Environ(), "ATTACHTEXT_MEASURE_PHASE="+phase)
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("phase %s %v: %v\n%s", phase, env, err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "GATE "); ok {
			var r map[string]any
			if err := json.Unmarshal([]byte(rest), &r); err != nil {
				t.Fatal(err)
			}
			return r
		}
	}
	t.Fatalf("phase %s: no result\n%s", phase, out)
	return nil
}

// procStatus returns a field of /proc/self/status in bytes.
func procStatus(t *testing.T, field string) uint64 {
	t.Helper()
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, field+":"); ok {
			kb, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(rest), " kB"), 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return kb << 10
		}
	}
	t.Fatalf("no %s", field)
	return 0
}

// resetPeak resets VmHWM to the current RSS (Linux 4.0 and later).
func resetPeak(t *testing.T) {
	t.Helper()
	if err := os.WriteFile("/proc/self/clear_refs", []byte("5"), 0); err != nil {
		t.Fatal(err)
	}
}

func settle() {
	runtime.GC()
	debug.FreeOSMemory()
}

func report(t *testing.T, r map[string]any) {
	b, _ := json.Marshal(r)
	fmt.Println("GATE " + string(b))
}

func measurePhase(t *testing.T, phase string) {
	e := New(nil)
	if s := os.Getenv("ATTACHTEXT_LIMIT_MIB"); s != "" {
		n, _ := strconv.Atoi(s)
		e.pdf.limit = uint64(n) << 20
	}
	small := pdftest.Text("warm up")
	ctx := context.Background()
	compile := func() {
		if _, err := e.PDF(ctx, small); err != nil {
			t.Fatal(err)
		}
	}
	switch phase {
	case "compile":
		settle()
		before := procStatus(t, "VmRSS")
		resetPeak(t)
		if s := os.Getenv("ATTACHTEXT_GCPERCENT"); s != "" {
			n, _ := strconv.Atoi(s)
			old := debug.SetGCPercent(n)
			defer debug.SetGCPercent(old)
		}
		start := time.Now()
		compile() // the first PDF: compile and the first instance
		secs := time.Since(start).Seconds()
		peak := procStatus(t, "VmHWM")
		debug.SetGCPercent(100)
		settle()
		after := procStatus(t, "VmRSS")
		report(t, map[string]any{"seconds": secs, "peakMiB": mib(peak - before), "afterMiB": mib(after - before)})
	case "doc":
		compile()
		settle()
		base := procStatus(t, "VmRSS")
		resetPeak(t)
		start := time.Now()
		data, err := os.ReadFile(os.Getenv("ATTACHTEXT_DOC"))
		if err != nil {
			t.Fatal(err)
		}
		res, err := e.PDF(ctx, data)
		if err != nil {
			t.Fatal(err)
		}
		secs := time.Since(start).Seconds()
		peak := procStatus(t, "VmHWM")
		report(t, map[string]any{"peakMiB": mib(peak - base), "baselineMiB": mib(base), "linearMiB": mib(e.pdf.peak.Load()), "status": res.Status, "seconds": secs})
	case "corpus":
		list, err := os.ReadFile(os.Getenv("ATTACHTEXT_LIST"))
		if err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(os.Getenv("ATTACHTEXT_OUT"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		enc := json.NewEncoder(f)
		for _, p := range strings.Split(string(list), "\n") {
			if p == "" {
				continue
			}
			data, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			start := time.Now()
			res, err := e.PDF(ctx, data)
			if err != nil {
				t.Fatal(err)
			}
			h := fnv.New64a() // compares texts between runs; no security role
			_, _ = h.Write([]byte(res.Text))
			_ = enc.Encode(corpusEntry{Path: p, Status: res.Status, Sum: fmt.Sprintf("%x", h.Sum64()), Peak: e.pdf.peak.Load(), Ms: float64(time.Since(start).Microseconds()) / 1000})
		}
		report(t, map[string]any{"ok": true})
	case "timing":
		compile()
		data, err := os.ReadFile(os.Getenv("ATTACHTEXT_DOC"))
		if err != nil {
			t.Fatal(err)
		}
		median := func(mem bool) float64 {
			e.pdf.memDoc = mem
			var ms []float64
			var text string
			for range 9 {
				start := time.Now()
				res, err := e.PDF(ctx, data)
				if err != nil {
					t.Fatal(err)
				}
				ms = append(ms, float64(time.Since(start).Microseconds())/1000)
				text = res.Text
			}
			if !bytes.Contains([]byte(text), []byte("0")) && !strings.Contains(text, "large") {
				t.Fatalf("no text: %q", text)
			}
			sort.Float64s(ms)
			return ms[len(ms)/2]
		}
		report(t, map[string]any{"customMs": median(false), "memMs": median(true)})
	default:
		t.Fatalf("phase %q", phase)
	}
}
