package events

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scriptUptimeAllowed — где в прод-коде может стоять ScriptUptime:
// "<путь>:<функция>". Поле t= хук-скрипта (В1, F595) — диагностика.
var scriptUptimeAllowed = map[string]bool{
	"internal/ndms/events/parse.go:ParseHookForm": true, // разбор t=
	"internal/api/hook.go:Handle":                 true, // Debug «hook age»
}

// TestScriptUptime_OnlyLogged — Event.ScriptUptime читается только для
// журнала: ни оркестратор, ни диспетчер, ни кэш по нему не решают. Любое
// упоминание идентификатора в прод-коде вне scriptUptimeAllowed (кроме
// объявления поля) — ошибка.
func TestScriptUptime_OnlyLogged(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	used := map[string]bool{}
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch rel {
			case "vendor", "frontend", "graphify-out", "docs", ".git", ".claude", "build":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !strings.Contains(string(data), "ScriptUptime") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, rel, data, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("разбор %s: %v", rel, err)
		}
		fields := map[*ast.Ident]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			if f, ok := n.(*ast.Field); ok {
				for _, id := range f.Names {
					fields[id] = true
				}
			}
			return true
		})
		for _, decl := range file.Decls {
			fn := ""
			if fd, ok := decl.(*ast.FuncDecl); ok {
				fn = fd.Name.Name
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				id, ok := n.(*ast.Ident)
				if !ok || id.Name != "ScriptUptime" || fields[id] {
					return true
				}
				key := rel + ":" + fn
				if scriptUptimeAllowed[key] {
					used[key] = true
					return true
				}
				t.Errorf("%s: ScriptUptime в %s — поле t= только для журнала (В1)", fset.Position(id.Pos()), key)
				return true
			})
		}
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
	for key := range scriptUptimeAllowed {
		if !used[key] {
			t.Errorf("%s: разрешение не использовано — сканер смотрит не туда", key)
		}
	}
}
