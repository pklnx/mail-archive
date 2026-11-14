package alertmail

import (
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/keepia/archive/internal/store"
)

// Languages of alert mail.
const (
	LangEN = "en"
	LangDE = "de"
)

// Language picks the language of a recipient's mail: the UI setting if it
// is German or English, else the UI's language when the address was set,
// else English.
func Language(setting, atCode string) string {
	for _, v := range []string{setting, atCode} {
		switch base, _, _ := strings.Cut(strings.ToLower(v), "-"); base {
		case LangDE, LangEN:
			return base
		}
	}
	return LangEN
}

// Alert is what one alert mail says: the event of the job's payload and
// the names read at delivery, already cleaned by Name. Error texts,
// folders, IMAP user names, hosts and mail data are never part of it.
type Alert struct {
	store.AlertMailPayload
	Account, Owner, Group string
	// PublicURL is KEEPIA_ARCHIVE_PUBLIC_URL; empty leaves the link out.
	PublicURL string
}

type texts struct {
	accountLabel, ownerLabel, groupLabel, eventLabel, openLink string
	noOwner, instance                                          string
	footer                                                     string
}

var textsByLang = map[string]texts{
	LangEN: {
		accountLabel: "Account", ownerLabel: "Owner", groupLabel: "Customer group", eventLabel: "Event ID", openLink: "Open",
		noOwner: "none", instance: "the instance",
		footer: "You get this mail as an admin of the mail archive with a confirmed address for alert mail.\n" +
			"Change or remove it in Profile > Notifications.",
	},
	LangDE: {
		accountLabel: "Konto", ownerLabel: "Besitzer", groupLabel: "Kundengruppe", eventLabel: "Ereignis-ID", openLink: "Öffnen",
		noOwner: "keiner", instance: "die Instanz",
		footer: "Du erhältst diese Mail als Admin des Mail-Archivs mit einer bestätigten Adresse für Alarm-Mails.\n" +
			"Ändern oder entfernen kannst du sie unter Profil > Benachrichtigungen.",
	},
}

// Texts returns the subject and body of a in lang (LangEN or LangDE).
func Texts(lang string, a Alert) (subject, body string) {
	de := lang == LangDE
	tx := textsByLang[LangEN]
	if de {
		tx = textsByLang[LangDE]
	}
	since := ""
	if a.Since != nil {
		since = a.Since.UTC().Format("2006-01-02 15:04 UTC")
	}
	acc := a.Account
	if acc == "" {
		acc = strconv.FormatInt(a.AlertMailPayload.Account, 10)
	}
	owner := a.Owner
	if owner == "" {
		owner = tx.noOwner
	}
	var what string
	link := ""
	switch a.Kind {
	case store.AlertMailSync, store.AlertMailFill:
		unit := map[bool]string{false: "syncs", true: "Synchronisierungen"}[de]
		if a.Kind == store.AlertMailFill {
			unit = map[bool]string{false: "recurring imports", true: "laufenden Importe"}[de]
		}
		if a.Level == "recovered" {
			if de {
				subject = fmt.Sprintf("Mail-Archiv: Konto „%s“ läuft wieder", acc)
				what = fmt.Sprintf("Die %s des Kontos „%s“ laufen wieder, nach Fehlern seit %s.", unit, acc, since)
			} else {
				subject = fmt.Sprintf("Mail archive: account \"%s\" works again", acc)
				what = fmt.Sprintf("The %s of account \"%s\" work again after failing since %s.", unit, acc, since)
			}
		} else {
			if de {
				subject = fmt.Sprintf("Mail-Archiv: %s des Kontos „%s“ schlagen fehl", capitalize(unit), acc)
				what = fmt.Sprintf("Die %s des Kontos „%s“ sind %d-mal in Folge fehlgeschlagen, seit %s. "+
					"Die Fehlermeldung steht in der Kontenübersicht.", unit, acc, a.Streak, since)
			} else {
				subject = fmt.Sprintf("Mail archive: %s of account \"%s\" keep failing", unit, acc)
				what = fmt.Sprintf("The %s of account \"%s\" failed %d times in a row, since %s. "+
					"The accounts page shows the error.", unit, acc, a.Streak, since)
			}
		}
		link = accountLink(a.PublicURL, a.Account)
	case store.AlertMailLoss:
		if de {
			subject = fmt.Sprintf("Mail-Archiv: %d Nachrichten auf dem Server gelöscht (Konto „%s“)", a.Lost, acc)
			what = fmt.Sprintf("%d von %d Nachrichten des Kontos „%s“ sind nicht mehr auf dem IMAP-Server (festgestellt %s). "+
				"Sie bleiben im Archiv.", a.Lost, a.PresentBefore, acc, since)
		} else {
			subject = fmt.Sprintf("Mail archive: %d messages deleted on the server (account \"%s\")", a.Lost, acc)
			what = fmt.Sprintf("%d of %d messages of account \"%s\" are no longer on the IMAP server (found %s). "+
				"They stay in the archive.", a.Lost, a.PresentBefore, acc, since)
		}
		link = accountLink(a.PublicURL, a.Account)
	case store.AlertMailStorage:
		subject, what = storageTexts(de, tx, a, since)
		link = a.PublicURL
	}
	var b strings.Builder
	b.WriteString(what)
	b.WriteString("\n\n")
	if a.Kind == store.AlertMailStorage {
		if a.AlertMailPayload.Group != nil {
			fmt.Fprintf(&b, "%s: %s\n", tx.groupLabel, groupName(a))
		}
	} else {
		fmt.Fprintf(&b, "%s: %s\n%s: %s\n", tx.accountLabel, acc, tx.ownerLabel, owner)
	}
	fmt.Fprintf(&b, "%s: %s\n", tx.eventLabel, a.Event)
	if link != "" {
		fmt.Fprintf(&b, "%s: %s\n", tx.openLink, link)
	}
	b.WriteString("\n-- \n")
	b.WriteString(tx.footer)
	b.WriteString("\n")
	return subject, b.String()
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

func groupName(a Alert) string {
	if a.Group != "" {
		return a.Group
	}
	if a.AlertMailPayload.Group != nil {
		return strconv.FormatInt(*a.AlertMailPayload.Group, 10)
	}
	return ""
}

func accountLink(public, account string) string {
	if public == "" || account == "" {
		return ""
	}
	return strings.TrimRight(public, "/") + "/?view=accounts&id=" + url.QueryEscape(account)
}

// storageTexts is the subject and the sentence of a storage transition.
func storageTexts(de bool, tx texts, a Alert, since string) (subject, what string) {
	scope := tx.instance
	scopeTitle := map[bool]string{false: "storage", true: "Speicher"}[de]
	if a.AlertMailPayload.Group != nil {
		if de {
			scope = fmt.Sprintf("die Kundengruppe „%s“", groupName(a))
			scopeTitle = fmt.Sprintf("Speicher der Gruppe „%s“", groupName(a))
		} else {
			scope = fmt.Sprintf("customer group \"%s\"", groupName(a))
			scopeTitle = fmt.Sprintf("storage of group \"%s\"", groupName(a))
		}
	}
	figures := humanBytes(a.UsedBytes)
	pct := ""
	if a.QuotaBytes > 0 {
		p := math.Floor(float64(a.UsedBytes)*1000/float64(a.QuotaBytes)) / 10
		pct = strconv.FormatFloat(p, 'f', -1, 64) + " %"
		if de {
			figures = fmt.Sprintf("%s von %s (%s)", humanBytes(a.UsedBytes), humanBytes(a.QuotaBytes), pct)
		} else {
			figures = fmt.Sprintf("%s of %s (%s)", humanBytes(a.UsedBytes), humanBytes(a.QuotaBytes), pct)
		}
	}
	prefix := map[bool]string{false: "Mail archive: ", true: "Mail-Archiv: "}[de]
	switch a.Level {
	case "disk_low":
		if de {
			return prefix + "Datenplatte unter ihrer Reserve",
				fmt.Sprintf("Der freie Platz der Datenplatte ist seit %s unter der Reserve. Neue Mail wird nicht archiviert; "+
					"SMTP-Journalkopien werden mit 452 zurückgestellt.", since)
		}
		return prefix + "data disk below its free-space reserve",
			fmt.Sprintf("The free space of the data disk is below its reserve since %s. New mail is not archived; "+
				"SMTP journal copies are deferred with 452.", since)
	case "ok":
		if de {
			return prefix + capitalize(scopeTitle) + " wieder normal",
				fmt.Sprintf("Der Speicher für %s ist seit %s wieder unter der Warnschwelle: %s belegt.", scope, since, figures)
		}
		return prefix + scopeTitle + " back to normal",
			fmt.Sprintf("Storage of %s is below the warning level again since %s: %s used.", scope, since, figures)
	case "warn":
		if de {
			return prefix + capitalize(scopeTitle) + " bei " + orLevel(pct, "Warnung"),
				fmt.Sprintf("Der Speicher für %s hat seit %s die Warnschwelle erreicht: %s belegt.", scope, since, figures)
		}
		return prefix + scopeTitle + " at " + orLevel(pct, "warning level"),
			fmt.Sprintf("Storage of %s reached the warning level since %s: %s used.", scope, since, figures)
	case "full":
		if de {
			return prefix + capitalize(scopeTitle) + " voll",
				fmt.Sprintf("Der Speicher für %s ist seit %s voll: %s belegt. Neue Mail aus IMAP-Synchronisierungen und aus Import-Läufen "+
					"wird nicht archiviert; SMTP-Journalkopien werden bis zum Überlauf noch angenommen.", scope, since, figures)
		}
		return prefix + scopeTitle + " full",
			fmt.Sprintf("Storage of %s is full since %s: %s used. New mail from IMAP syncs and imports is not archived; "+
				"SMTP journal copies are still accepted up to the overflow.", scope, since, figures)
	case "overflow":
		if de {
			return prefix + capitalize(scopeTitle) + " übergelaufen",
				fmt.Sprintf("Der Speicher für %s ist seit %s über dem Überlauf: %s belegt. Neue Mail wird nicht archiviert; "+
					"SMTP-Journalkopien werden mit 452 zurückgestellt.", scope, since, figures)
		}
		return prefix + scopeTitle + " overflowing",
			fmt.Sprintf("Storage of %s is past the overflow since %s: %s used. New mail is not archived; "+
				"SMTP journal copies are deferred with 452.", scope, since, figures)
	}
	return prefix + scopeTitle + " " + a.Level, fmt.Sprintf("%s: %s since %s, %s used.", scope, a.Level, since, figures)
}

func orLevel(pct, level string) string {
	if pct != "" {
		return pct
	}
	return level
}

// humanBytes writes n in binary units with one decimal, such as "1.5 GiB".
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}

// CodeTexts are the subject and body of a confirmation code mail.
func CodeTexts(lang, code, publicURL string, ttl time.Duration) (subject, body string) {
	mins := int(ttl / time.Minute)
	where := ""
	if publicURL != "" {
		where = " (" + publicURL + ")"
	}
	if lang == LangDE {
		return "Mail-Archiv: Bestätigungscode " + code,
			fmt.Sprintf("Dein Bestätigungscode für Alarm-Mails des Mail-Archivs%s:\n\n    %s\n\n"+
				"Gib ihn innerhalb von %d Minuten unter Profil > Benachrichtigungen ein.\n"+
				"Wenn du diese Adresse nicht eingetragen hast, ignoriere diese Mail.\n", where, code, mins)
	}
	return "Mail archive: confirmation code " + code,
		fmt.Sprintf("Your confirmation code for alert mail of the mail archive%s:\n\n    %s\n\n"+
			"Enter it within %d minutes in Profile > Notifications.\n"+
			"If you did not enter this address, ignore this mail.\n", where, code, mins)
}

// TestTexts are the subject and body of `./ma notify mail-test`.
func TestTexts(now time.Time) (subject, body string) {
	return "Mail archive: test message",
		"This is a test message of the mail archive's alert mail, sent by `notify mail-test` at " +
			now.UTC().Format("2006-01-02 15:04 UTC") + ".\n"
}
