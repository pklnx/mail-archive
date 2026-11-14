package notify_test

import (
	"context"
	"fmt"
	"mime"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/keepia/archive/internal/notify"
	"github.com/keepia/archive/internal/store"
)

// hostile are values that would be markup, a mention, a link, a new line
// or reordered text if an alert showed them as they are.
var hostile = []string{
	"<!channel>",
	"<https://x.example|click>",
	"[click](https://x.example)",
	"@everyone",
	"<@123>",
	"*b* _i_ ~s~ ||s||",
	"a`b",
	"x\nAccount \"x\" synced successfully again",
	"x\r\nTitle: forged",
	"x\u0085y",
	"x\u2028y",
	"x\u2029y",
	"x\u202ey",
	"=?UTF-8?b?eAp5?=",
	"a & b <c>",
}

var testSince = time.Date(2026, 10, 20, 8, 0, 0, 0, time.UTC)

// builders build a message of each kind with v in every value it shows.
var builders = map[string]func(v string) *notify.Message{
	"sync failing": func(v string) *notify.Message {
		return notify.BuildMessage([]store.PendingNotification{syncPending(1, v, store.NotifiedOK)}, true)
	},
	"sync recovered": func(v string) *notify.Message {
		return notify.BuildMessage([]store.PendingNotification{syncPending(1, v, store.NotifiedFailing)}, true)
	},
	"sync mixed": func(v string) *notify.Message {
		return notify.BuildMessage([]store.PendingNotification{syncPending(1, v, store.NotifiedOK), syncPending(2, v, store.NotifiedFailing)}, true)
	},
	"fill failing": func(v string) *notify.Message {
		return notify.BuildFillMessage([]store.PendingNotification{fillPending(1, v, store.NotifiedOK)}, true)
	},
	"fill recovered": func(v string) *notify.Message {
		return notify.BuildFillMessage([]store.PendingNotification{fillPending(1, v, store.NotifiedFailing)}, true)
	},
	"fill mixed": func(v string) *notify.Message {
		return notify.BuildFillMessage([]store.PendingNotification{fillPending(1, v, store.NotifiedOK), fillPending(2, v, store.NotifiedFailing)}, true)
	},
	"loss": func(v string) *notify.Message {
		return notify.BuildLossMessage([]store.PendingLossAlert{lossPending(1, v)}, true)
	},
	"loss batch": func(v string) *notify.Message {
		return notify.BuildLossMessage([]store.PendingLossAlert{lossPending(1, v), lossPending(2, v)}, true)
	},
}

func syncPending(id int64, v, state string) store.PendingNotification {
	return store.PendingNotification{
		AccountID: id, Account: v, Owner: v, FailureStreak: 3, FailingSince: &testSince, NotifiedState: state, LastError: v,
	}
}

func fillPending(id int64, v, state string) store.PendingNotification {
	p := syncPending(id, v, state)
	p.FillID, p.Mailbox = id+10, v
	return p
}

func lossPending(id int64, v string) store.PendingLossAlert {
	return store.PendingLossAlert{
		ID: id, SyncRunID: id, AccountID: id, Account: v, Owner: v,
		LossAlert: store.LossAlert{Lost: 120, PresentBefore: 200, MoreFolders: 2,
			Folders: []store.FolderLoss{{Name: v, Lost: 100}, {Name: v, Lost: 20}}},
	}
}

// sent is one message as the JSON receivers and ntfy got it.
type sent struct {
	title, message, text, content string
	json                          map[string]any
	ntfyTitleRaw, ntfyTitle       string
	ntfyBody                      string
	ntfyHeaders                   map[string][]string
}

func send(t *testing.T, m *notify.Message) sent {
	t.Helper()
	jsonRcv, ntfyRcv := newReceiver(t), newReceiver(t)
	for _, w := range []*notify.Webhook{webhook(t, jsonRcv.srv.URL, "", ""), webhook(t, ntfyRcv.srv.URL, "ntfy", "")} {
		if _, err := w.Send(context.Background(), m); err != nil {
			t.Fatal(err)
		}
	}
	j, n := jsonRcv.got()[0], ntfyRcv.got()[0]
	var s sent
	s.json = j.json
	s.title, _ = j.json["title"].(string)
	s.message, _ = j.json["message"].(string)
	s.text, _ = j.json["text"].(string)
	s.content, _ = j.json["content"].(string)
	s.ntfyTitleRaw = n.header.Get("Title")
	title, err := new(mime.WordDecoder).DecodeHeader(s.ntfyTitleRaw)
	if err != nil {
		t.Fatalf("ntfy title %q: %v", s.ntfyTitleRaw, err)
	}
	s.ntfyTitle, s.ntfyBody, s.ntfyHeaders = title, string(n.body), n.header
	return s
}

var codeSpan = regexp.MustCompile("`[^`\n]*`")

// skeleton is s with every code span replaced by a marker: the constant
// text around the values.
func skeleton(s string) string { return codeSpan.ReplaceAllString(s, "`V`") }

// wantPlain is how a value must look in plain text: characters that break
// or reorder a line become spaces.
func wantPlain(v string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, v)
}

// lineBreaker finds a character that breaks or reorders a line, other
// than the message's own line feeds.
func lineBreaker(s string) (rune, bool) {
	for _, r := range s {
		if r != '\n' && (unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) || unicode.In(r, unicode.Zl, unicode.Zp)) {
			return r, true
		}
	}
	return 0, false
}

// TestAlertValuesAreLiteral: in every builder and every field, a hostile
// value adds no line, no markup, no mention and no link: it is plain
// text in title, message and ntfy, and one closed code span in text and
// content, with Slack's entities escaped.
func TestAlertValuesAreLiteral(t *testing.T) {
	for kind, build := range builders {
		benign := send(t, build("work"))
		for _, v := range hostile {
			t.Run(fmt.Sprintf("%s/%q", kind, v), func(t *testing.T) {
				m := build(v)
				got := send(t, m)
				for field, pair := range map[string][2]string{
					"title": {got.title, benign.title}, "message": {got.message, benign.message},
					"ntfy title": {got.ntfyTitle, benign.ntfyTitle}, "ntfy body": {got.ntfyBody, benign.ntfyBody},
					"text": {got.text, benign.text}, "content": {got.content, benign.content},
				} {
					if r, bad := lineBreaker(pair[0]); bad {
						t.Errorf("%s holds %U: %q", field, r, pair[0])
					}
					if a, b := strings.Count(pair[0], "\n"), strings.Count(pair[1], "\n"); a != b {
						t.Errorf("%s has %d line breaks, want %d: %q", field, a, b, pair[0])
					}
				}
				if got.ntfyTitle != got.title || got.ntfyBody != got.message {
					t.Errorf("ntfy %q / %q, want %q / %q", got.ntfyTitle, got.ntfyBody, got.title, got.message)
				}
				if !strings.Contains(got.title, "Mail archive: ") || strings.Contains(benign.title, "work") && !strings.Contains(got.title, wantPlain(v)) {
					t.Errorf("title %q does not show %q", got.title, wantPlain(v))
				}
				for field, s := range map[string][2]string{"text": {got.text, benign.text}, "content": {got.content, benign.content}} {
					if skeleton(s[0]) != skeleton(s[1]) {
						t.Errorf("%s: values are not one code span each:\n%q\nwant the form of\n%q", field, s[0], s[1])
					}
				}
				if strings.ContainsAny(got.text, "<>") || strings.Count(got.text, "&") != strings.Count(got.text, "&amp;")+
					strings.Count(got.text, "&lt;")+strings.Count(got.text, "&gt;") {
					t.Errorf("text has raw <, > or &: %q", got.text)
				}
				for _, h := range []string{"Markdown", "X-Markdown", "Md"} {
					if _, ok := got.ntfyHeaders[h]; ok {
						t.Errorf("ntfy got a %s header", h)
					}
				}
				if ev := got.json["accounts"].([]any)[0].(map[string]any); ev["account"] != v || ev["owner"] != v {
					t.Errorf("accounts changed the value: %v", ev)
				}
			})
		}
	}
}

// TestAlertBodyPinsPlainAndNoMentions: Discord pings no one, Gotify shows
// plain text.
func TestAlertBodyPinsPlainAndNoMentions(t *testing.T) {
	for _, m := range []*notify.Message{builders["sync failing"]("@everyone"), notify.TestMessage(time.Now())} {
		j := send(t, m).json
		am, _ := j["allowed_mentions"].(map[string]any)
		if parse, ok := am["parse"].([]any); !ok || len(parse) != 0 {
			t.Errorf("allowed_mentions %v", j["allowed_mentions"])
		}
		ex, _ := j["extras"].(map[string]any)
		if cd, _ := ex["client::display"].(map[string]any); cd["contentType"] != "text/plain" {
			t.Errorf("extras %v", j["extras"])
		}
	}
}

// TestNtfyTitleIsEncoded: the title is always made of encoded words of at
// most 75 characters, split between runes and joined by one space, so a
// literal encoded word in a value decodes to itself.
func TestNtfyTitleIsEncoded(t *testing.T) {
	for _, v := range []string{"=?UTF-8?b?eAp5?=", "work", strings.Repeat("ä", 40), strings.Repeat("😀", 30) + "x" + strings.Repeat("€", 30)} {
		m := builders["sync failing"](v)
		got := send(t, m)
		if got.ntfyTitle != m.Title || got.ntfyTitle != "Mail archive: sync of "+v+" keeps failing" {
			t.Errorf("decoded title %q, want %q", got.ntfyTitle, m.Title)
		}
		words := strings.Split(got.ntfyTitleRaw, " ")
		if len(m.Title) > 45 && len(words) < 2 {
			t.Errorf("one word for %d bytes: %q", len(m.Title), got.ntfyTitleRaw)
		}
		for _, w := range words {
			dec, err := new(mime.WordDecoder).Decode(w)
			if len(w) > 75 || !strings.HasPrefix(w, "=?UTF-8?b?") || err != nil || !utf8.ValidString(dec) {
				t.Errorf("word %q (%d characters): %q, %v", w, len(w), dec, err)
			}
		}
	}
}

// checkFitted checks that s is within limit runes and every code span in
// it is closed; in text, that no entity is cut.
func checkFitted(t *testing.T, field, s string, limit int) {
	t.Helper()
	if n := utf8.RuneCountInString(s); n > limit {
		t.Errorf("%s has %d runes, limit %d", field, n, limit)
	}
	for i, line := range strings.Split(s, "\n") {
		if strings.Count(line, "`")%2 != 0 {
			t.Errorf("%s line %d has an open code span: %q", field, i, line)
		}
	}
	if strings.HasPrefix(field, "text") && strings.Count(s, "&") != strings.Count(s, "&amp;")+strings.Count(s, "&lt;")+strings.Count(s, "&gt;") {
		t.Errorf("%s has a cut entity", field)
	}
}

// TestLongValueStraddlesTheLimit: a value whose rendering crosses the
// limit of content (Discord) or text (Slack, Mattermost) is shortened
// before it is wrapped, so the span stays closed; a value made of & grows
// fivefold in text.
func TestLongValueStraddlesTheLimit(t *testing.T) {
	cases := []struct {
		field string
		limit int
		char  string
		from  int
		to    int
	}{
		{"content", 2000, "x", 900, 1000},
		{"content", 2000, "&", 900, 1000},
		{"text", 16000, "x", 7900, 8000},
		{"text", 16000, "&", 1550, 1650},
	}
	for _, c := range cases {
		shortened, whole := false, false
		for n := c.from; n <= c.to; n++ {
			v := strings.Repeat(c.char, n)
			p := syncPending(1, v, store.NotifiedOK)
			p.Owner, p.LastError = "kim", "e"
			got := send(t, notify.BuildMessage([]store.PendingNotification{p}, true))
			s := map[string]string{"content": got.content, "text": got.text}[c.field]
			checkFitted(t, fmt.Sprintf("%s with %d×%s", c.field, n, c.char), s, c.limit)
			if !strings.HasSuffix(s, "Last error: `e`") {
				t.Fatalf("%s with %d×%s: entry cut at its end: %q", c.field, n, c.char, s)
			}
			if strings.Contains(s, "…`") {
				shortened = true
			} else {
				whole = true
			}
		}
		if !shortened || !whole {
			t.Errorf("%s with %s does not straddle the limit: shortened %v, whole %v", c.field, c.char, shortened, whole)
		}
	}
}

// TestBatchIsFittedByEntries: 50 entries with 200-rune errors do not fit
// in content or text; whole entries are dropped and counted.
func TestBatchIsFittedByEntries(t *testing.T) {
	pending := make([]store.PendingNotification, 50)
	for i := range pending {
		pending[i] = syncPending(int64(i+1), fmt.Sprintf("acc%d", i), store.NotifiedOK)
		pending[i].LastError = strings.Repeat("&<`", 66) + "&<" // 200 runes, the most kept
	}
	got := send(t, notify.BuildMessage(pending, true))
	if n := len(got.json["accounts"].([]any)); n != 50 {
		t.Fatalf("%d accounts", n)
	}
	more := regexp.MustCompile("\n\n… and ([0-9]+) more \\(see accounts\\)$")
	for field, c := range map[string]struct {
		s     string
		limit int
	}{"content": {got.content, 2000}, "text": {got.text, 16000}} {
		checkFitted(t, field, c.s, c.limit)
		shown := strings.Count(c.s, "Account `")
		mm := more.FindStringSubmatch(c.s)
		if shown == 0 || mm == nil || mm[1] != fmt.Sprint(50-shown) {
			t.Errorf("%s: %d shown, closing %v: %q", field, shown, mm, c.s)
		}
		if strings.Contains(c.s, "…`") {
			t.Errorf("%s: an entry was cut instead of dropped", field)
		}
	}
	if !strings.Contains(got.message, "acc49") {
		t.Error("message lost entries")
	}
}
