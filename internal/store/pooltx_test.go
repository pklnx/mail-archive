package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// poolFields are the Store fields that reach the connection pool.
var poolFields = []string{"q", "pool"}

// TestNoPoolInTransactions checks that code running with a transaction's
// queries never uses the pool: no s.q or s.pool, and no call of a Store
// method that uses them, directly or through other methods. A transaction
// holds a connection and possibly row locks; waiting for a second
// connection there can deadlock once every connection belongs to a
// transaction waiting for those locks (#74). The code checked is every
// function or function literal with a *db.Queries parameter: the functions
// passed to inTx and inWriteTx, and the helpers they call. Not checked: the
// few functions that open a transaction themselves (inTx,
// WithAttestSnapshot, RebuildLeaves) between Begin and Commit; keep pool
// calls there out, or after the rollback, as inTx does.
func TestNoPoolInTransactions(t *testing.T) {
	fset := token.NewFileSet()
	var files []*ast.File
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	users := poolUsers(files)
	if !users["Audit"] || !users["inTx"] {
		t.Fatalf("pool users not found: %v", users)
	}
	checked := 0
	for _, f := range files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			recv := receiverName(fd)
			if hasQueriesParam(fd.Type) {
				checked++
				for _, p := range poolUses(fd.Body, recv, users) {
					t.Errorf("%s: %s uses the pool (%s) but runs with a transaction's queries", fset.Position(p.Pos()), fd.Name.Name, describe(p))
				}
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				lit, ok := n.(*ast.FuncLit)
				if !ok || !hasQueriesParam(lit.Type) {
					return true
				}
				checked++
				for _, p := range poolUses(lit.Body, recv, users) {
					t.Errorf("%s: a transaction in %s uses the pool (%s)", fset.Position(p.Pos()), fd.Name.Name, describe(p))
				}
				return false
			})
		}
	}
	if checked < 20 {
		t.Fatalf("only %d functions with a *db.Queries parameter found", checked)
	}
}

// poolUsers returns the Store methods that use the pool, directly or
// through other Store methods.
func poolUsers(files []*ast.File) map[string]bool {
	direct := map[string]bool{}
	calls := map[string][]string{}
	for _, f := range files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil || !isStoreMethod(fd) {
				continue
			}
			recv := receiverName(fd)
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || !isIdent(sel.X, recv) {
					return true
				}
				if slices.Contains(poolFields, sel.Sel.Name) {
					direct[fd.Name.Name] = true
				} else {
					calls[fd.Name.Name] = append(calls[fd.Name.Name], sel.Sel.Name)
				}
				return true
			})
		}
	}
	for changed := true; changed; {
		changed = false
		for m, callees := range calls {
			if direct[m] {
				continue
			}
			if slices.ContainsFunc(callees, func(c string) bool { return direct[c] }) {
				direct[m], changed = true, true
			}
		}
	}
	return direct
}

// poolUses returns the expressions in body that reach the pool through the
// receiver recv: recv.q, recv.pool, or a call of a method in users.
func poolUses(body *ast.BlockStmt, recv string, users map[string]bool) []*ast.SelectorExpr {
	if recv == "" {
		return nil
	}
	var out []*ast.SelectorExpr
	ast.Inspect(body, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if ok && isIdent(sel.X, recv) && (slices.Contains(poolFields, sel.Sel.Name) || users[sel.Sel.Name]) {
			out = append(out, sel)
		}
		return true
	})
	return out
}

func describe(sel *ast.SelectorExpr) string {
	return sel.X.(*ast.Ident).Name + "." + sel.Sel.Name
}

func isStoreMethod(fd *ast.FuncDecl) bool {
	if fd.Recv == nil || len(fd.Recv.List) != 1 {
		return false
	}
	star, ok := fd.Recv.List[0].Type.(*ast.StarExpr)
	return ok && isIdent(star.X, "Store")
}

func receiverName(fd *ast.FuncDecl) string {
	if !isStoreMethod(fd) || len(fd.Recv.List[0].Names) == 0 {
		return ""
	}
	return fd.Recv.List[0].Names[0].Name
}

// hasQueriesParam reports whether a function takes a *db.Queries.
func hasQueriesParam(ft *ast.FuncType) bool {
	for _, p := range ft.Params.List {
		star, ok := p.Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		if sel, ok := star.X.(*ast.SelectorExpr); ok && isIdent(sel.X, "db") && sel.Sel.Name == "Queries" {
			return true
		}
	}
	return false
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}
