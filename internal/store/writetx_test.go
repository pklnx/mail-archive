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
	"strings"
	"testing"
)

// writesAttested matches SQL that inserts into a table attestations cover.
var writesAttested = regexp.MustCompile(`(?i)insert\s+into\s+(messages|message_deletions)\b`)

// attestedWriteQueries returns the sqlc queries whose SQL matches
// writesAttested.
func attestedWriteQueries(t *testing.T) map[string]bool {
	t.Helper()
	files, err := filepath.Glob("queries/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatal("no queries", err)
	}
	names := map[string]bool{}
	nameRe := regexp.MustCompile(`(?m)^-- name: (\w+)`)
	for _, f := range files {
		b, err := os.ReadFile(f) //nolint:gosec // test reads the package's own files
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		idx := nameRe.FindAllStringSubmatchIndex(src, -1)
		for i, m := range idx {
			end := len(src)
			if i+1 < len(idx) {
				end = idx[i+1][0]
			}
			if writesAttested.MatchString(src[m[0]:end]) {
				names[src[m[2]:m[3]]] = true
			}
		}
	}
	if !names["InsertMessage"] {
		t.Fatalf("InsertMessage not found among %v", names)
	}
	return names
}

// TestAttestedWritesTakeFence checks that every insert into messages or
// message_deletions runs in a function passed to inWriteTx, so it holds
// the attestation fence, and that the locks are taken in the order blob
// lock, fence: inWriteTx takes the blob lock first, and nothing takes it
// inside one.
func TestAttestedWritesTakeFence(t *testing.T) {
	queries := attestedWriteQueries(t)
	fset := token.NewFileSet()
	pkgFiles, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, path := range pkgFiles {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		var stack []ast.Node
		ast.Inspect(f, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			stack = append(stack, n)
			pos := fset.Position(n.Pos())
			switch n := n.(type) {
			case *ast.CallExpr:
				name := calledName(n)
				switch {
				case queries[name]:
					found++
					if !inWriteTxFunc(stack) {
						t.Errorf("%s: %s outside a function passed to inWriteTx", pos, name)
					}
				case (name == "LockBlobsShared" || name == "TryLockBlobsShared") && inWriteTxFunc(stack):
					t.Errorf("%s: the blob lock is taken inside inWriteTx, after the fence", pos)
				case name == "LockFenceShared" && enclosingFunc(stack) != "inWriteTx":
					t.Errorf("%s: the fence is taken outside inWriteTx", pos)
				}
			case *ast.BasicLit:
				if n.Kind == token.STRING && writesAttested.MatchString(n.Value) && !inWriteTxFunc(stack) {
					t.Errorf("%s: SQL that inserts into an attested table outside inWriteTx", pos)
				}
			case *ast.FuncDecl:
				if n.Name.Name == "inWriteTx" {
					checkLockOrder(t, fset, n)
				}
			}
			return true
		})
	}
	if found == 0 {
		t.Fatal("no calls of the attested write queries found")
	}
}

// TestNoAttestedWritesElsewhere checks that no other package calls the
// attested write queries directly, bypassing the store.
func TestNoAttestedWritesElsewhere(t *testing.T) {
	queries := attestedWriteQueries(t)
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	here, _ := filepath.Abs(".")
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == here || d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".") && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		b, err := os.ReadFile(path) //nolint:gosec // test reads the repository's own files
		if err != nil {
			return err
		}
		for name := range queries {
			if strings.Contains(string(b), "."+name+"(") {
				t.Errorf("%s calls %s; insert through the store, which takes the attestation fence", path, name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func calledName(c *ast.CallExpr) string {
	switch fn := c.Fun.(type) {
	case *ast.SelectorExpr:
		return fn.Sel.Name
	case *ast.Ident:
		return fn.Name
	}
	return ""
}

// inWriteTxFunc reports whether the innermost function literal on the
// stack is an argument of a call to inWriteTx.
func inWriteTxFunc(stack []ast.Node) bool {
	for i := len(stack) - 1; i > 0; i-- {
		lit, ok := stack[i].(*ast.FuncLit)
		if !ok {
			continue
		}
		call, ok := stack[i-1].(*ast.CallExpr)
		return ok && calledName(call) == "inWriteTx" && slices.Contains(call.Args, ast.Expr(lit))
	}
	return false
}

func enclosingFunc(stack []ast.Node) string {
	for i := len(stack) - 1; i >= 0; i-- {
		if fd, ok := stack[i].(*ast.FuncDecl); ok {
			return fd.Name.Name
		}
	}
	return ""
}

// checkLockOrder checks that inWriteTx takes the blob lock, tried and
// waited for, before the fence.
func checkLockOrder(t *testing.T, fset *token.FileSet, fd *ast.FuncDecl) {
	t.Helper()
	var try, blob, fence token.Pos
	ast.Inspect(fd, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			switch calledName(c) {
			case "TryLockBlobsShared":
				try = c.Pos()
			case "LockBlobsShared":
				blob = c.Pos()
			case "LockFenceShared":
				fence = c.Pos()
			}
		}
		return true
	})
	if try == token.NoPos || blob == token.NoPos || fence == token.NoPos || try > fence || blob > fence {
		t.Errorf("%s: inWriteTx must take the blob lock, then the fence", fset.Position(fd.Pos()))
	}
}
