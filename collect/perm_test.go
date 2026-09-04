package collect

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// Nothing this package creates may be group- or world-readable.
//
// What the collection produces is the estate's inventory: instance and
// database names, principals and role memberships, backup history, error log
// excerpts, query text — and, by the admission of 50.agent/020.job-steps.sql
// itself, possibly a password sitting in the first 200 characters of a T-SQL
// job step. The .zip beside it is the same content, and it is the one that
// gets mailed onward.
//
// config.go already makes this argument, for .env, in the comment on
// WriteEnvTemplate: 0600 rather than 0644, "because it is easier to widen a
// file than to notice it was world-readable for the fortnight before anyone
// looked". It was never applied to what the run writes, which is the larger
// prize of the two. On a shared Linux jump host every local account could read
// output/.
//
// Asserted on the source rather than on a created file because the mode is
// what this fixes: Windows, which is most of this tool's users, largely
// ignores the bits and inherits the parent directory's ACL instead. A test
// that stat'ed a file would pass there for the wrong reason and skip, which
// leaves the rule unenforced on the platform where it is checked.
func TestNothingThisPackageCreatesIsWorldReadable(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, name, nil, 0)
		if perr != nil {
			t.Fatalf("parsing %s: %v", name, perr)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "os" {
				return true
			}
			switch sel.Sel.Name {
			case "Mkdir", "MkdirAll", "WriteFile", "OpenFile":
			case "Create":
				// os.Create is 0666 before umask and takes no argument to say
				// otherwise. The archive is created with it.
				t.Errorf("%s: os.Create cannot express a mode; use os.OpenFile with 0600",
					fset.Position(call.Pos()))
				return true
			default:
				return true
			}
			if len(call.Args) == 0 {
				return true
			}
			arg := call.Args[len(call.Args)-1]
			if id, isIdent := arg.(*ast.Ident); isIdent {
				if id.Name != "dirPerm" && id.Name != "filePerm" {
					t.Errorf("%s: %s takes its mode from %s; use dirPerm or filePerm",
						fset.Position(arg.Pos()), sel.Sel.Name, id.Name)
				}
				return true
			}
			lit, ok := arg.(*ast.BasicLit)
			if !ok {
				return true
			}
			mode, cerr := strconv.ParseUint(strings.TrimPrefix(strings.TrimPrefix(lit.Value, "0o"), "0"), 8, 32)
			if cerr != nil {
				return true
			}
			if mode&0o077 != 0 {
				t.Errorf("%s: %s creates with mode %s, readable by other accounts on the machine",
					fset.Position(lit.Pos()), sel.Sel.Name, lit.Value)
			}
			return true
		})
	}
}
