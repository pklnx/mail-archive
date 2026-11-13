package alertmail_test

import (
	"strings"
	"testing"
	"time"

	"github.com/pklnx/mail-archive/internal/alertmail"
	"github.com/pklnx/mail-archive/internal/store"
)

var since = time.Date(2026, 11, 12, 8, 30, 0, 0, time.UTC)

func payloadSync() store.AlertMailPayload {
	return store.AlertMailPayload{Event: "3-1762936200-failing", Kind: store.AlertMailSync, User: 1, Account: 3, Level: "failing", Since: &since, Streak: 4}
}

func TestLanguage(t *testing.T) {
	for _, c := range [][3]string{
		{"de", "en", "de"}, {"en", "de", "en"}, {"auto", "de", "de"}, {"auto", "", "en"}, {"fr", "", "en"},
		{"fr", "de", "de"}, {"de-AT", "", "de"}, {"", "", "en"},
	} {
		if got := alertmail.Language(c[0], c[1]); got != c[2] {
			t.Errorf("Language(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

// TestTexts: each kind names its account, owner or group and the event,
// with the link; storage mails name the group; the figures are right.
func TestTexts(t *testing.T) {
	group := int64(7)
	for _, c := range []struct {
		name string
		a    alertmail.Alert
		lang string
		want []string
	}{
		{"sync failing en", alertmail.Alert{AlertMailPayload: payloadSync(), Account: "Büro", Owner: "kim", PublicURL: "https://a.example"},
			alertmail.LangEN, []string{`syncs of account "Büro" keep failing`, "failed 4 times in a row, since 2026-11-12 08:30 UTC",
				"Owner: kim", "Event ID: 3-1762936200-failing", "Open: https://a.example/?view=accounts&id=B%C3%BCro"}},
		{"sync recovered de", alertmail.Alert{AlertMailPayload: store.AlertMailPayload{Event: "x", Kind: store.AlertMailSync, Account: 3, Level: "recovered", Since: &since},
			Account: "Büro"}, alertmail.LangDE, []string{"Konto „Büro“ läuft wieder", "Besitzer: keiner"}},
		{"fill en", alertmail.Alert{AlertMailPayload: store.AlertMailPayload{Event: "fill-2-1-failing", Kind: store.AlertMailFill, Account: 3, Fill: 2, Level: "failing", Since: &since, Streak: 3},
			Account: "journal", Owner: "kim"}, alertmail.LangEN, []string{`recurring imports of account "journal" keep failing`}},
		{"loss de", alertmail.Alert{AlertMailPayload: store.AlertMailPayload{Event: "gone-9", Kind: store.AlertMailLoss, Account: 3, Since: &since, Lost: 40, PresentBefore: 100},
			Account: "Büro", Owner: "kim"}, alertmail.LangDE, []string{"40 Nachrichten auf dem Server gelöscht", "40 von 100 Nachrichten"}},
		{"group warn en", alertmail.Alert{AlertMailPayload: store.AlertMailPayload{Event: "storage:7:1", Kind: store.AlertMailStorage, Group: &group, Level: "warn", Since: &since,
			UsedBytes: 820, QuotaBytes: 1000}, Group: "Müller GmbH"}, alertmail.LangEN,
			[]string{`storage of group "Müller GmbH" at 82 %`, `customer group "Müller GmbH" reached the warning level`, "820 B of 1000 B (82 %)", "Customer group: Müller GmbH"}},
		{"instance full de", alertmail.Alert{AlertMailPayload: store.AlertMailPayload{Event: "storage:instance:2", Kind: store.AlertMailStorage, Level: "full", Since: &since,
			UsedBytes: 2048, QuotaBytes: 2048}}, alertmail.LangDE, []string{"Mail-Archiv: Speicher voll", "die Instanz ist seit"}},
		{"disk low en", alertmail.Alert{AlertMailPayload: store.AlertMailPayload{Event: "storage:instance:3", Kind: store.AlertMailStorage, Level: "disk_low", Since: &since}},
			alertmail.LangEN, []string{"data disk below its free-space reserve"}},
	} {
		subject, body := alertmail.Texts(c.lang, c.a)
		all := subject + "\n" + body
		for _, w := range c.want {
			if !strings.Contains(all, w) {
				t.Errorf("%s: %q missing in\n%s", c.name, w, all)
			}
		}
		if strings.ContainsAny(subject, "\r\n") {
			t.Errorf("%s: subject %q", c.name, subject)
		}
	}
}
