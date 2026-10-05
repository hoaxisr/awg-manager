package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestWiring_SwapGateShared — барьер D-N1 работает, только если стор
// интерфейсов (читатель), бэкенд, роутер (снос fakeip-tun) и сироты (через
// бэкенд) держат ОДИН экземпляр netdev.SwapGate. Свой экземпляр у любого из
// них компилируется и молчит: подмена шла бы мимо списков. Проводке нужен
// живой роутер, поэтому проверяется исходник: экземпляров два — процесса
// (a.swapGate, wiring_core.go) и уборки (gate, cleanup.go), — и каждый
// потребитель получает один из них.
// Мутация: backend.NewKernel(&netdev.SwapGate{}) в wiring_tunnels.go → красный.
func TestWiring_SwapGateShared(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	render := func(fset *token.FileSet, e ast.Expr) string {
		var b bytes.Buffer
		_ = printer.Fprint(&b, fset, e)
		return b.String()
	}
	var gates, kernels, deps, orphans []string // "<файл>: <выражение>"
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CompositeLit:
				if n.Type != nil && render(fset, n.Type) == "netdev.SwapGate" {
					gates = append(gates, name)
				}
			case *ast.CallExpr:
				if render(fset, n.Fun) == "backend.NewKernel" && len(n.Args) == 1 {
					kernels = append(kernels, name+": "+render(fset, n.Args[0]))
				}
			case *ast.KeyValueExpr:
				switch render(fset, n.Key) {
				case "SwapGate":
					deps = append(deps, name+": "+render(fset, n.Value))
				case "OrphanNDMS":
					orphans = append(orphans, name+": "+render(fset, n.Value))
				}
			}
			return true
		})
	}
	slices.Sort(gates)
	slices.Sort(kernels)
	slices.Sort(deps)
	check := func(what string, got, want []string) {
		t.Helper()
		if !slices.Equal(got, want) {
			t.Errorf("%s:\n got  %q\n want %q", what, got, want)
		}
	}
	check("экземпляры netdev.SwapGate", gates, []string{"cleanup.go", "wiring_core.go"})
	check("backend.NewKernel", kernels, []string{"cleanup.go: gate", "wiring_tunnels.go: a.swapGate"})
	check("поля SwapGate (query.Deps, router.Deps)", deps, []string{
		"cleanup.go: gate", "cleanup.go: gate", "wiring_core.go: a.swapGate", "wiring_server.go: a.swapGate",
	})
	if len(orphans) != 1 || !strings.Contains(orphans[0], "a.backendImpl") {
		t.Errorf("сироты сносят устройство не бэкендом процесса: %q", orphans)
	}
}
