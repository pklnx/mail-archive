package imapsync_test

import (
	"context"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/pklnx/mail-archive/internal/imapsync"
	"github.com/pklnx/mail-archive/internal/imaptest"
)

func TestListFoldersDelimiter(t *testing.T) {
	for _, tt := range []struct {
		name  string
		delim rune
		want  string
	}{
		{"slash", '/', "/"},
		{"dot", '.', "."},
		{"NIL", 0, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			u := imapmemserver.NewUser("u", "pw")
			imaptest.CreateMailboxes(t, u, "INBOX")
			host, port := imaptest.StartWithDelimiter(t, tt.delim, map[string]imap.MailboxAttr{"INBOX": ""}, u)
			conn, err := imapsync.Dial(context.Background(), imapsync.Config{
				Host: host, Port: port, TLSMode: imapsync.TLSModeNone, Username: "u", Password: "pw",
			})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			folders, err := conn.ListFolders()
			if err != nil {
				t.Fatal(err)
			}
			if len(folders) != 1 || folders[0].Delim != tt.want {
				t.Fatalf("folders = %+v, want delimiter %q", folders, tt.want)
			}
		})
	}
}
