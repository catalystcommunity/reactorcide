package priority

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEverySubmitPathAssignsPriority makes sure that every function that
// stores a new job calls priority.Assign or priority.AssignRetry before the
// store call, so all submit paths agree (docs/priority.md). A new submit
// path that does not call it fails here.
func TestEverySubmitPathAssignsPriority(t *testing.T) {
	root := filepath.Join("..")
	skipDirs := map[string]bool{"postgres_store": true, "priority": true, "csilapi": true}
	found := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if skipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			var create, assign token.Pos
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				// A store call: x.store.CreateJob(ctx, job) or st.CreateJob(ctx, job).
				// The REST handler method JobHandler.CreateJob takes (w, r).
				if sel.Sel.Name == "CreateJob" && len(call.Args) == 2 && create == token.NoPos {
					if ident, ok := call.Args[1].(*ast.Ident); !ok || ident.Name != "r" {
						create = call.Pos()
					}
				}
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "priority" && (sel.Sel.Name == "Assign" || sel.Sel.Name == "AssignRetry") && assign == token.NoPos {
					assign = call.Pos()
				}
				return true
			})
			if create == token.NoPos {
				continue
			}
			found++
			where := fset.Position(create).String()
			if assert.NotEqual(t, token.NoPos, assign, "%s stores a job without priority.Assign", where) {
				assert.Less(t, assign, create, "%s: priority.Assign must come before CreateJob", where)
			}
		}
		return nil
	})
	require.NoError(t, err)
	// handlers (job, PR, push, schedule), worker (trigger, workflow node),
	// and jobcontrol (retry).
	assert.GreaterOrEqual(t, found, 6, "the scan found the submit paths")
}
