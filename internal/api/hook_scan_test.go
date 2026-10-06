package api

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

// TestClaimOwn_OnlyInHookSink — кредиты своих хуков гасит одно место: claimOwn
// точки входа spool (П22), по одному вызову на вид. Второе гашение в прод-коде
// (диспетчер, оркестратор) съело бы кредит второй раз — следующий свой хук
// стал бы чужим — или вынесло бы вердикт не в порядке прихода.
// Мутация: второй вызов ClaimOwnCreated в events/dispatcher.go → красный.
func TestClaimOwn_OnlyInHookSink(t *testing.T) {
	const site = "internal/api/hook.go:claimOwn"
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil || !strings.Contains(string(data), "ClaimOwn") {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			for _, decl := range file.Decls {
				key := filepath.ToSlash(rel) + ":"
				if fd, ok := decl.(*ast.FuncDecl); ok {
					key += fd.Name.Name
				}
				ast.Inspect(decl, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && strings.HasPrefix(sel.Sel.Name, "ClaimOwn") {
						got[sel.Sel.Name] = append(got[sel.Sel.Name], key)
					}
					return true
				})
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"ClaimOwnCreated", "ClaimOwnDestroyed", "ClaimOwnConf"} {
		if keys := got[name]; len(keys) != 1 || keys[0] != site {
			t.Errorf("%s вызывается %v, ждали ровно один раз в %s", name, keys, site)
		}
		delete(got, name)
	}
	for name, keys := range got {
		t.Errorf("неизвестное гашение %s в %v", name, keys)
	}
}
