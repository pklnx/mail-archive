package archive

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestBlobLockOnPoolOnlyInVerify (#166): Store.TryLockBlobs holds the
// exclusive blob lock on a pool connection of its own, and the code under
// it uses the pool. In the web server, writers waiting for the lock may
// hold every other connection, so jobs there take it through a lock
// session (store.DeletionSession, store.PurgeSession, store.BlobSession).
// Only verify, which runs in a process without blob writers, takes it
// through waitBlobLock. Test files are not checked: they take the lock to
// make writers wait.
func TestBlobLockOnPoolOnlyInVerify(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	storeDir, _ := filepath.Abs("../store")
	fset := token.NewFileSet()
	checked := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == storeDir || d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".") && path != root {
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
		checked++
		rel, _ := filepath.Rel(root, path)
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fn := call.Fun.(type) {
				case *ast.Ident:
					if fn.Name == "waitBlobLock" && rel != filepath.Join("internal", "archive", "verify.go") {
						t.Errorf("%s: %s calls waitBlobLock; take the blob lock through a lock session", fset.Position(call.Pos()), fd.Name.Name)
					}
				case *ast.SelectorExpr:
					if fn.Sel.Name != "TryLockBlobs" {
						return true
					}
					if x, ok := fn.X.(*ast.Ident); ok && x.Name == "sess" {
						return true // a lock session's
					}
					if fd.Name.Name != "waitBlobLock" {
						t.Errorf("%s: %s calls TryLockBlobs on the store; take the blob lock through a lock session", fset.Position(call.Pos()), fd.Name.Name)
					}
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 20 {
		t.Fatalf("only %d files checked", checked)
	}
}
