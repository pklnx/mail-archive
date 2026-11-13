package notify

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/pklnx/mail-archive/internal/config"
)

func TestSendTimesOut(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	cfg, err := config.ParseWebhook(srv.URL, "", "")
	if err != nil {
		t.Fatal(err)
	}
	w := NewWebhook(cfg)
	if w.client.Timeout != RequestTimeout {
		t.Fatalf("timeout %v", w.client.Timeout)
	}
	w.client.Timeout = 100 * time.Millisecond
	start := time.Now()
	_, err = w.Send(context.Background(), TestMessage(start))
	var de *DeliveryError
	if !errors.As(err, &de) || de.Status != 0 || de.Permanent() {
		t.Fatalf("err %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %v", d)
	}
}

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	for v, want := range map[string]time.Duration{
		"":                              0,
		"120":                           2 * time.Minute,
		"-5":                            0,
		"soon":                          0,
		"Thu, 08 Oct 2026 12:10:00 GMT": 10 * time.Minute,
		"Thu, 08 Oct 2026 11:00:00 GMT": 0,
	} {
		if got := retryAfter(v, now); got != want {
			t.Errorf("retryAfter(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("äöüß", 3); got != "äö…" {
		t.Fatal(got)
	}
	if got := truncate("abc", 3); got != "abc" {
		t.Fatal(got)
	}
}

// TestShortenCutsConstantsLast: when values cut to an ellipsis still do
// not fit, constant text is cut, the line break after the title and the
// closing line stay, and every span stays closed.
func TestShortenCutsConstantsLast(t *testing.T) {
	d := &draft{
		title:   []piece{constant("Mail archive: sync of "), value("work"), constant(" keeps failing")},
		entries: [][]piece{{constant("Account "), quoted("work"), constant(" (owner "), value("anna"), constant(", ID 3) failed.")}, {constant("x")}},
	}
	for _, limit := range []int{112, 80, 60} {
		got := d.fit(styleSlack, limit)
		if n := utf8.RuneCountInString(got); n > limit || strings.Count(got, "`")%2 != 0 ||
			!strings.Contains(got, "\n") || !strings.HasSuffix(got, "\n\n… and 1 more (see accounts)") {
			t.Errorf("limit %d: %q (%d runes)", limit, got, n)
		}
	}
	// 115 runes in full: the values are cut first, to the longest common
	// length that fits, and the constants stay.
	if got := d.fit(styleSlack, 112); !strings.Contains(got, "`wo…`") || !strings.Contains(got, "`an…`") || !strings.Contains(got, " keeps failing\nAccount `") ||
		!strings.Contains(got, ", ID 3) failed.") {
		t.Errorf("values are not cut before constants: %q", got)
	}
}
