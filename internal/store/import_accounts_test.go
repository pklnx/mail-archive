package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

func TestImportAccounts(t *testing.T) {
	st, url := storetest.NewWithURL(t)
	ctx := context.Background()

	imp := &store.Account{Kind: store.KindImport, Name: "old mail"}
	if err := st.CreateAccount(ctx, imp); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetAccount(ctx, imp.ID)
	if err != nil || got.Kind != store.KindImport || got.Port != 0 || got.Enabled {
		t.Fatalf("import account: %+v, %v", got, err)
	}
	if a := createAccount(t, st, "imap"); a.Kind != store.KindIMAP {
		t.Errorf("default kind %q", a.Kind)
	}
	for _, bad := range []*store.Account{
		{Kind: store.KindImport, Name: "x", Enabled: true},
		{Kind: store.KindImport, Name: "y", Port: 993},
		{Kind: store.KindImport, Name: "z", Host: "h"},
		{Kind: "pop3", Name: "w", Port: 110},
	} {
		if err := st.CreateAccount(ctx, bad); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}

	// The database refuses what the Go checks refuse.
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	for _, sql := range []string{
		`UPDATE accounts SET enabled = true WHERE kind = 'import'`,
		`UPDATE accounts SET port = 993 WHERE kind = 'import'`,
		`UPDATE accounts SET port = 0 WHERE kind = 'imap'`,
		`UPDATE accounts SET kind = 'other'`,
	} {
		if _, err := conn.Exec(ctx, sql); err == nil {
			t.Errorf("database accepted %s", sql)
		}
	}

	// Only a rename is allowed.
	enabled, name := true, "older mail"
	for _, c := range []store.AccountChange{
		{Enabled: &enabled},
		{Folders: &store.FolderFilters{Excluded: []string{"x"}}},
		{PasswordEnc: []byte{1}},
		{Connection: &store.Connection{Host: "h", Port: 993, TLSMode: store.TLSModeTLS, Username: "u"}},
	} {
		if err := st.UpdateAccount(ctx, imp.Ref(), c); !errors.Is(err, store.ErrImportAccount) {
			t.Errorf("change %+v: %v", c, err)
		}
	}
	if err := st.UpdateAccount(ctx, imp.Ref(), store.AccountChange{Name: &name}); err != nil {
		t.Fatal(err)
	}

	stats, _, err := st.Stats(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]store.AccountKind{}
	for _, s := range stats {
		kinds[s.Account] = s.Kind
	}
	if kinds["older mail"] != store.KindImport || kinds["imap"] != store.KindIMAP {
		t.Errorf("stats kinds %v", kinds)
	}

	// LocationInFolder.
	folder, err := st.GetOrCreateFolder(ctx, imp.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("c", 64)
	if ok, err := st.LocationInFolder(ctx, folder.ID, hash); err != nil || ok {
		t.Fatalf("before: %v %v", ok, err)
	}
	meta := store.MessageMeta{SHA256: hash, Size: 1, StoredPath: "p"}
	if _, err := st.SaveBatch(ctx, folder.ID, 1, []store.MessageMeta{meta}, []store.Location{{FolderID: folder.ID, UIDValidity: 1, UID: 1}}); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.LocationInFolder(ctx, folder.ID, hash); err != nil || !ok {
		t.Fatalf("after: %v %v", ok, err)
	}

	// Rolling back is refused while import accounts exist.
	if err := migrateDownThrough(st, "00011_import_accounts.sql"); err == nil || !strings.Contains(err.Error(), "import accounts exist") {
		t.Fatalf("migrate down: %v", err)
	}
}
