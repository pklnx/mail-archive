package store

import "context"

// UserRelations counts the tables, views, sequences and other relations
// outside the system schemas: 0 for a database that was just created.
// restore-dump loads a dump only into such a database (#392, part 4).
func (s *Store) UserRelations(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname NOT IN ('pg_catalog', 'information_schema')
		  AND n.nspname NOT LIKE 'pg\_toast%' AND n.nspname NOT LIKE 'pg\_temp\_%'`).Scan(&n)
	return n, err
}
