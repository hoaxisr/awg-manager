package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// parseMainFile — разбор файла пакета main из исходника: проводка ниже
// проверяется чтением кода, потому что собрать *app целиком в тесте нечем.
func parseMainFile(t *testing.T, name string) (*token.FileSet, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	return fset, f
}

// Б1 ревью плана: ветка «не бут» в startBootSequence выполняется в ГЛАВНОЙ
// горутине до a.serve(), а бут прокси-рантайма теперь ждёт загрузки бинарей
// (F98, до 5 мин на файл). Синхронный нудж встал бы на manager.bootMu за этой
// загрузкой и держал бы фазу бута и HTTP всё это время; у cold-boot он к тому
// же стоит до bootDone. Поэтому все три вызова обязаны идти горутиной —
// снятое `go` иначе зелёное.
func TestProxyRuntimeNudgeGuard_BootCallsAreAsync(t *testing.T) {
	fset, f := parseMainFile(t, "boot.go")

	// Позиции вызовов, стоящих ПОД go — сравниваем по ним, а не по
	// родителю: ast.Inspect родителя не даёт.
	async := map[token.Pos]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		if g, ok := n.(*ast.GoStmt); ok {
			async[g.Call.Pos()] = true
		}
		return true
	})

	found := 0
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "proxyRuntimeNudge" {
			return true
		}
		found++
		if !async[call.Pos()] {
			t.Errorf("%s: proxyRuntimeNudge вызван синхронно — фаза бута встанет на загрузке бинарей", fset.Position(call.Pos()))
		}
		return true
	})
	if found != 3 {
		t.Fatalf("в boot.go найдено %d нуджей, ждали 3 (cold-boot, post-restore, daemon-restart)", found)
	}
}

// Б2 ревью плана: цикл повтора бута вооружается ТОЛЬКО здесь — это
// единственная точка, где виден ErrBinariesPending от Boot (все вызывающие
// Boot идут через proxyRuntimeNudge). Без armBinariesRetry отложенный бут
// ждал бы WAN-up-нуджа или перезапуска демона, а прогон остался бы зелёным.
func TestProxyRuntimeNudgeGuard_ArmsBinariesRetry(t *testing.T) {
	_, f := parseMainFile(t, "wiring_proxyrt.go")

	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "proxyRuntimeNudge" || fn.Recv == nil {
			continue
		}
		armed := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "armBinariesRetry" {
				armed = true
			}
			return true
		})
		if !armed {
			t.Fatal("proxyRuntimeNudge не зовёт armBinariesRetry — цикл повтора после ErrBinariesPending не заводится")
		}
		return
	}
	t.Fatal("метод proxyRuntimeNudge в wiring_proxyrt.go не найден")
}
