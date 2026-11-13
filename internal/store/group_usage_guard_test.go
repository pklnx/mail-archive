package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Group usage stays an upper bound between snapshots only if every
// statement that adds a message to an account goes through
// recordGroupUsage (#397, part 1, revision 4 point 1). The guard below
// derives the writers from the SQL, so a new one cannot slip past it.

// groupUsageCovered are the queries that add locations and whose callers
// lock the account's group first (lockAccountGroup) and record the group
// delta after (recordGroupUsage).
var groupUsageCovered = []string{"UpsertLocation", "InsertReceivedLocation", "InsertFilledLocation"}

// groupUsageExempt are writers that cannot add a message new to an
// account, with the reason and the only functions allowed to call them.
var groupUsageExempt = map[string]struct {
	reason  string
	callers []string
}{
	"ConvertLegacyFillBatch": {
		reason: "copies locations from the folders of a pre-#112 fill into the journal folder of the same account; " +
			"FillFolder creates those folders in the fill's own account, and only hashes they hold are copied, " +
			"so nothing new to the account (or its group) is added",
		callers: []string{"convertLegacyBatch"},
	},
}

// locationWriterPatterns match a statement (lower case, comments removed,
// whitespace collapsed) that adds locations: an insert, a merge or a copy.
var locationWriterPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\binsert into (public\.)?message_locations\b`),
	regexp.MustCompile(`\bmerge into (public\.)?message_locations\b`),
	regexp.MustCompile(`\bcopy (public\.)?message_locations\b`),
}

// locationMovers are the updates that add a message to an account without
// an insert: a location moved to another folder or message, or a folder
// moved to another account. Each table names the columns that must not be
// set.
var (
	updateOf = regexp.MustCompile(`\bupdate (?:only )?(?:public\.)?(message_locations|folders)\b(?: (?:as )?[a-z_][a-z0-9_]*)? set `)
	setEnd   = regexp.MustCompile(` (where|from|returning) `)
	movedBy  = map[string]*regexp.Regexp{
		"message_locations": regexp.MustCompile(`(?:^|,) ?(?:[a-z_][a-z0-9_]*\.)?(folder_id|message_sha256) ?=`),
		"folders":           regexp.MustCompile(`(?:^|,) ?(?:[a-z_][a-z0-9_]*\.)?account_id ?=`),
	}
)

var (
	sqlLineComment  = regexp.MustCompile(`--[^\n]*`)
	sqlBlockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	sqlSpace        = regexp.MustCompile(`\s+`)
	sqlQueryName    = regexp.MustCompile(`(?m)^-- name: (\w+) :\w+`)
)

// normalizeSQL lower-cases sql, removes comments and collapses whitespace.
func normalizeSQL(sql string) string {
	sql = sqlBlockComment.ReplaceAllString(sql, " ")
	sql = sqlLineComment.ReplaceAllString(sql, " ")
	return strings.TrimSpace(sqlSpace.ReplaceAllString(strings.ToLower(sql), " "))
}

// writesLocations reports whether sql (normalized) adds locations or
// moves them, or a folder, to another account.
func writesLocations(sql string) bool {
	for _, p := range locationWriterPatterns {
		if p.MatchString(sql) {
			return true
		}
	}
	for _, m := range updateOf.FindAllStringSubmatchIndex(sql, -1) {
		set := sql[m[1]:]
		if end := setEnd.FindStringIndex(set); end != nil {
			set = set[:end[0]]
		}
		if movedBy[sql[m[2]:m[3]]].MatchString(set) {
			return true
		}
	}
	return false
}

// locationWriterQueries returns the names of the sqlc queries in dir that
// write locations (writesLocations).
func locationWriterQueries(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	for name, sql := range queryTextsByName(t, dir) {
		if writesLocations(sql) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// queryTextsByName returns the sqlc queries in dir by name, normalized
// (normalizeSQL).
func queryTextsByName(t *testing.T, dir string) map[string]string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no queries in %s: %v", dir, err)
	}
	out := map[string]string{}
	for _, path := range paths {
		data, err := os.ReadFile(path) //nolint:gosec // the package's own query files
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		idx := sqlQueryName.FindAllStringSubmatchIndex(text, -1)
		for i, m := range idx {
			end := len(text)
			if i+1 < len(idx) {
				end = idx[i+1][0]
			}
			out[text[m[2]:m[3]]] = normalizeSQL(text[m[1]:end])
		}
	}
	return out
}

func TestGroupUsageGuardSQL(t *testing.T) {
	found := locationWriterQueries(t, "queries")
	for _, name := range found {
		_, exempt := groupUsageExempt[name]
		if !slices.Contains(groupUsageCovered, name) && !exempt {
			t.Errorf("query %s writes message_locations (or moves a folder): call it after lockAccountGroup and before recordGroupUsage "+
				"and add it to groupUsageCovered, or add it to groupUsageExempt with the reason it adds nothing new to an account", name)
		}
	}
	for _, name := range append(slices.Clone(groupUsageCovered), mapsKeys(groupUsageExempt)...) {
		if !slices.Contains(found, name) {
			t.Errorf("%s is listed in the guard but writes no location any more: remove it from the list", name)
		}
	}
}

func mapsKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// moduleGoFiles parses the non-test Go files under cmd and internal of the
// module, except the generated queries.
func moduleGoFiles(t *testing.T) (*token.FileSet, map[string]*ast.File) {
	t.Helper()
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	for _, top := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "node_modules" || d.Name() == "testdata" || path == filepath.Join(root, "internal", "store", "db") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			files[path] = f
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(files) < 50 {
		t.Fatalf("only %d Go files found", len(files))
	}
	return fset, files
}

// TestGroupUsageGuardRawSQL: no Go string outside the generated queries
// writes locations; writers are sqlc queries, which the SQL check sees.
func TestGroupUsageGuardRawSQL(t *testing.T) {
	fset, files := moduleGoFiles(t)
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if writesLocations(normalizeSQL(s)) {
				t.Errorf("%s: raw SQL writes message_locations (or moves a folder); make it a query in internal/store/queries", fset.Position(lit.Pos()))
			}
			return true
		})
	}
}

// TestGroupUsageGuardCallers: every reference to a covered query sits in a
// function (the innermost function literal or declaration: the transaction
// closure) that calls lockAccountGroup before it and before any folder
// lock or AdvanceFolder, and recordGroupUsage after it. An exempt query is
// referenced only from its allowed functions.
func TestGroupUsageGuardCallers(t *testing.T) {
	fset, files := moduleGoFiles(t)
	checked := 0
	for _, f := range files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			var stack []ast.Node
			ast.Inspect(fd, func(n ast.Node) bool {
				if n == nil {
					stack = stack[:len(stack)-1]
					return true
				}
				stack = append(stack, n)
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				name := sel.Sel.Name
				pos := fset.Position(sel.Pos())
				if ex, ok := groupUsageExempt[name]; ok {
					checked++
					if !slices.Contains(ex.callers, fd.Name.Name) {
						t.Errorf("%s: %s is exempt from the group usage guard only in %v (%s), not in %s", pos, name, ex.callers, ex.reason, fd.Name.Name)
					}
					return true
				}
				if !slices.Contains(groupUsageCovered, name) {
					return true
				}
				checked++
				body := innermostBody(stack)
				lock := firstCall(body, "lockAccountGroup")
				switch {
				case lock == token.NoPos:
					t.Errorf("%s: %s in %s without lockAccountGroup in the same function", pos, name, fd.Name.Name)
				case lock > sel.Pos():
					t.Errorf("%s: %s in %s before lockAccountGroup", pos, name, fd.Name.Name)
				}
				for _, other := range []string{"LockFolder", "LockFolderByID", "AdvanceFolder"} {
					if p := firstCall(body, other); p != token.NoPos && lock != token.NoPos && p < lock {
						t.Errorf("%s: %s in %s runs before lockAccountGroup", fset.Position(p), other, fd.Name.Name)
					}
				}
				if rec := lastCall(body, "recordGroupUsage"); rec == token.NoPos || rec < sel.Pos() {
					t.Errorf("%s: %s in %s without recordGroupUsage after it in the same function", pos, name, fd.Name.Name)
				}
				return true
			})
		}
	}
	if checked < len(groupUsageCovered)+len(groupUsageExempt) {
		t.Fatalf("found only %d references to the guarded queries", checked)
	}
}

// innermostBody returns the body of the innermost function on the stack.
func innermostBody(stack []ast.Node) *ast.BlockStmt {
	for i := len(stack) - 1; i >= 0; i-- {
		switch fn := stack[i].(type) {
		case *ast.FuncLit:
			return fn.Body
		case *ast.FuncDecl:
			return fn.Body
		}
	}
	return nil
}

// calls returns the positions of the calls of name (a function or a
// method) in body, outside nested function literals.
func calls(body *ast.BlockStmt, name string) []token.Pos {
	var out []token.Pos
	if body == nil {
		return nil
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch c := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.CallExpr:
			switch fn := c.Fun.(type) {
			case *ast.Ident:
				if fn.Name == name {
					out = append(out, c.Pos())
				}
			case *ast.SelectorExpr:
				if fn.Sel.Name == name {
					out = append(out, c.Pos())
				}
			}
		}
		return true
	})
	return out
}

func firstCall(body *ast.BlockStmt, name string) token.Pos {
	if c := calls(body, name); len(c) > 0 {
		return c[0]
	}
	return token.NoPos
}

func lastCall(body *ast.BlockStmt, name string) token.Pos {
	if c := calls(body, name); len(c) > 0 {
		return c[len(c)-1]
	}
	return token.NoPos
}

// TestGroupUsageGuardMigrations: a migration after 00041 that writes
// locations requests a usage snapshot in the same migration (the group
// figures would miss what it adds).
func TestGroupUsageGuardMigrations(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("migrations", "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		n, err := strconv.Atoi(strings.SplitN(filepath.Base(path), "_", 2)[0])
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if n <= 41 {
			continue
		}
		data, err := os.ReadFile(path) //nolint:gosec // the package's own migrations
		if err != nil {
			t.Fatal(err)
		}
		up, _, _ := strings.Cut(string(data), "-- +goose Down")
		sql := normalizeSQL(up)
		if writesLocations(sql) && !strings.Contains(sql, "update storage_refresh_requests set seq = seq + 1") {
			t.Errorf("%s writes message_locations: add UPDATE storage_refresh_requests SET seq = seq + 1 and enqueue usage_refresh", path)
		}
	}
}

// TestGroupUsageGuardPatterns: the patterns match what they are for, and
// not the statements that only mark locations gone or seen.
func TestGroupUsageGuardPatterns(t *testing.T) {
	for _, sql := range []string{
		"INSERT INTO message_locations (message_sha256) VALUES ($1)",
		"insert into public.message_locations select * from x",
		"WITH x AS (SELECT 1) INSERT INTO message_locations AS l (a) SELECT 1",
		"MERGE INTO message_locations l USING x ON true WHEN MATCHED THEN DO NOTHING",
		"UPDATE message_locations SET folder_id = $1 WHERE id = $2",
		"UPDATE message_locations ml SET flags = $1, message_sha256 = $2",
		"UPDATE folders f SET name = $1, account_id = $2 WHERE id = $3",
		"COPY message_locations FROM STDIN",
	} {
		if !writesLocations(normalizeSQL(sql)) {
			t.Errorf("not flagged: %s", sql)
		}
	}
	for _, sql := range []string{
		"UPDATE message_locations SET gone_at = now() WHERE folder_id = $1",
		"UPDATE message_locations l SET last_seen_at = now(), flags = $2 FROM x WHERE l.folder_id = x.id",
		"UPDATE folders SET last_uid = $2 WHERE id = $1 AND account_id = $3",
		"SELECT * FROM message_locations -- INSERT INTO message_locations",
		"DELETE FROM message_locations WHERE folder_id = $1",
	} {
		if writesLocations(normalizeSQL(sql)) {
			t.Errorf("flagged: %s", sql)
		}
	}
}
