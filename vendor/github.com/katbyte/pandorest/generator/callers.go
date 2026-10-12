package generator

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/katbyte/pandorest/definitions"
)

// Uncalled lists the generated methods of a service that no hand-written Go file under root names, with how many methods there
// are: what the repository's own code never reaches, for a reader deciding what no tool wraps. A name counts wherever it is used
// as a selector, so a tool calling c.Items.GetItems counts, and a method of the same name on another type counts too, since the
// parse does not resolve types; a test file does not count, since a test naming a method is not a tool wrapping it. Generated
// files, by their header, are left out, as is what is under skip.
func Uncalled(svc *definitions.Service, root string, skip ...string) (uncalled []string, total int, err error) {
	used, err := selectorNames(root, skip)
	if err != nil {
		return nil, 0, err
	}
	for _, o := range svc.Operations() {
		names := []string{o.Name}
		if o.Pageable != nil {
			names = append(names, o.Name+"Complete", o.Name+"Each")
		}
		if o.Command != "" {
			names = append(names, o.Name+"AndWait")
		}
		if o.Commands != nil {
			names = append(names, "WaitForCommand")
		}
		for _, name := range names {
			total++
			if !used[name] {
				uncalled = append(uncalled, name)
			}
		}
	}
	slices.Sort(uncalled)

	return uncalled, total, nil
}

// selectorNames is every name used as a selector (x.Name) in the hand-written Go files under root, outside the directories in skip.
func selectorNames(root string, skip []string) (map[string]bool, error) {
	names := map[string]bool{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			if name := e.Name(); path != root && (strings.HasPrefix(name, ".") || name == "vendor" || name == "testdata" || slices.ContainsFunc(skip, func(s string) bool { return s != "" && sameDir(path, s) })) {
				return filepath.SkipDir
			}

			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path) //nolint:gosec // the repository's own files
		if err != nil {
			return err
		}
		if Generated(src) {
			return nil
		}
		f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				names[sel.Sel.Name] = true
			}

			return true
		})

		return nil
	})

	return names, err
}

// sameDir reports whether two paths name one directory.
func sameDir(a, b string) bool {
	a, errA := filepath.Abs(a)
	b, errB := filepath.Abs(b)

	return errA == nil && errB == nil && a == b
}
