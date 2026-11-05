package store_test

import (
	"context"
	"testing"

	"github.com/pklnx/mail-archive/internal/store"
	"github.com/pklnx/mail-archive/internal/store/storetest"
)

// UserRelations is 0 for a new database and counts the schema once
// migrated: restore-dump loads a dump only into the former.
func TestUserRelations(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.NewUnmigrated(t))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if n, err := st.UserRelations(ctx); err != nil || n != 0 {
		t.Fatalf("new database: %d, %v", n, err)
	}
	if _, err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := st.UserRelations(ctx); err != nil || n == 0 {
		t.Errorf("migrated database: %d, %v", n, err)
	}
}
