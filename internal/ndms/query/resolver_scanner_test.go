package query

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// systemNameLiteralAllowed — где в прод-файлах пакета может стоять литерал
// "system-name" (элемент пути запроса резолвера): сам резолвер и разбор
// payload в фейках, которые лежат в прод-файлах ради других пакетов.
var systemNameLiteralAllowed = map[string]bool{
	"interfaces.go:resolveSystemNames": true,
	"interfaces.go:fetchSystemName":    true,
	"fakendms.go:post":                 true,
	"getter.go:extractShowSystemName":  true,
}

// TestResolver_OnlyAfterList — резолвер system-name ходит в NDMS только вслед
// за свежим полным списком (F570): вызов resolveSystemNames ровно один и он в
// refreshList, fetchSystemName зовётся только из resolveSystemNames. Ленивый
// резолвер «по требованию» спрашивал имя, которое уже могли снять, — E в
// журнале ndm. Литерал "system-name" — только в systemNameLiteralAllowed.
func TestResolver_OnlyAfterList(t *testing.T) {
	calls := map[string][]string{} // вызываемая → функции, откуда зовут
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			where := name + ":"
			if fd, ok := decl.(*ast.FuncDecl); ok {
				where += fd.Name.Name
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				if lit, ok := n.(*ast.BasicLit); ok && lit.Value == `"system-name"` && !systemNameLiteralAllowed[where] {
					t.Errorf("%s: литерал \"system-name\" вне резолвера — запрос имени по требованию (F570)", fset.Position(lit.Pos()))
				}
				return true
			})
			fd, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fd, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				var callee string
				switch fn := call.Fun.(type) {
				case *ast.SelectorExpr:
					callee = fn.Sel.Name
				case *ast.Ident:
					callee = fn.Name
				}
				if callee == "resolveSystemNames" || callee == "fetchSystemName" {
					calls[callee] = append(calls[callee], fd.Name.Name)
				}
				return true
			})
		}
	}
	if got := calls["resolveSystemNames"]; len(got) != 1 || got[0] != "refreshList" {
		t.Errorf("resolveSystemNames зовут из %v, want ровно один вызов из refreshList", got)
	}
	for _, from := range calls["fetchSystemName"] {
		if from != "resolveSystemNames" {
			t.Errorf("fetchSystemName зовут из %s, want только из resolveSystemNames", from)
		}
	}
}
