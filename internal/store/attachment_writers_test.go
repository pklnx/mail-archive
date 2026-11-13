package store

import (
	"strings"
	"testing"
)

// TestAttachmentTextWriters: every statement that inserts locations names
// attachment_text_ok (#539, part 2), so that none takes the column's
// default false without the save queueing the message's job
// (attachmentflag.go). Like TestGroupUsageGuardSQL, it derives the
// writers from the SQL, so a new one cannot slip past it.
func TestAttachmentTextWriters(t *testing.T) {
	texts := map[string]string{}
	for _, name := range locationWriterQueries(t, "queries") {
		texts[name] = ""
	}
	for name, sql := range queryTextsByName(t, "queries") {
		if _, ok := texts[name]; !ok {
			continue
		}
		inserts := false
		for _, p := range locationWriterPatterns {
			inserts = inserts || p.MatchString(sql)
		}
		if inserts && !strings.Contains(sql, "attachment_text_ok") {
			t.Errorf("query %s inserts locations without setting attachment_text_ok", name)
		}
	}
}
