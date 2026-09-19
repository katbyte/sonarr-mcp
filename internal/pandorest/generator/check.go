package generator

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/katbyte/sonarr-mcp/internal/pandorest/definitions"
)

// Check verifies that the package in dir has a method on Client for every
// operation in the definitions (and a Complete method for every list), and
// returns how many operations it found.
func Check(svc *definitions.Service, dir string) (int, error) {
	methods, err := clientMethods(dir)
	if err != nil {
		return 0, err
	}

	var missing []string
	ops := svc.Operations()
	for _, o := range ops {
		if !methods[o.Name] {
			missing = append(missing, fmt.Sprintf("%s (%s)", o.Name, o.Key()))
		}
		if o.Pageable != nil && !methods[o.Name+"Complete"] {
			missing = append(missing, fmt.Sprintf("%sComplete (%s)", o.Name, o.Key()))
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return len(ops), fmt.Errorf("%s: %d methods missing from %s:\n  %s", svc.Name, len(missing), dir, strings.Join(missing, "\n  "))
	}

	return len(ops), nil
}

// clientMethods returns the names of the methods declared on Client across
// the non-test Go files in dir.
func clientMethods(dir string) (map[string]bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	methods := map[string]bool{}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
				continue
			}
			recv := fn.Recv.List[0].Type
			if star, ok := recv.(*ast.StarExpr); ok {
				recv = star.X
			}
			if id, ok := recv.(*ast.Ident); ok && id.Name == "Client" {
				methods[fn.Name.Name] = true
			}
		}
	}

	return methods, nil
}
