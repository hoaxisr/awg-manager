package router

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// Страж порядка: `defer res.Close()` обязан стоять ДО регистрации отката.
//
// Свойство поведением не проверить. Окно между выдачей номера и записью
// владения узкое, наблюдаемых вызовов в нём нет, а на пути отката номер уже
// защищён собственной записью настроек — соседний проситель попадает в это
// окно только на живом роутере. При этом обе поломки дёшевы и правдоподобны:
// «упростить» defer до немедленного Close и переставить его ниже отката.
// Поэтому страж разбирает исходник, как это делают инвентарные стражи ключей
// SSE и полей карточки туннеля.
//
// Порядок несущий: defer'ы идут в обратном порядке регистрации, значит
// зарегистрированный ПЕРВЫМ отработает ПОСЛЕДНИМ — сперва откат снимает
// созданное, и только потом номер уходит в оборот.
//
// Оба defer'а опознаются ПРИЦЕЛЬНО, а не по форме. По форме страж и молчал бы
// на чужом `defer x.Close()`, и падал бы на любом безобидном defer-замыкании
// (таймер, лог на выходе), обвиняя код в том, чего в нём нет.
func TestEnableClosesReservationAfterRollback(t *testing.T) {
	for _, c := range []struct{ file, fn string }{
		{"fakeip_enable.go", "enableFakeIPTun"},
		{"policytun_enable.go", "enablePolicyTun"},
	} {
		t.Run(c.fn, func(t *testing.T) {
			body := funcBody(t, c.file, c.fn)
			closeAt, rollbackAt := -1, -1
			for i, st := range body.List {
				d, ok := st.(*ast.DeferStmt)
				if !ok {
					continue
				}
				switch call := d.Call.Fun.(type) {
				case *ast.SelectorExpr: // defer res.Close()
					if closeAt < 0 && call.Sel.Name == "Close" && isIdent(call.X, "res") {
						closeAt = i
					}
				case *ast.FuncLit: // defer func(){ откат }()
					if rollbackAt < 0 && mentions(call.Body, "rollback") {
						rollbackAt = i
					}
				}
			}
			if closeAt < 0 {
				t.Fatal("резервация не закрывается `defer res.Close()`: номер утечёт до перезапуска демона")
			}
			if rollbackAt < 0 {
				t.Fatal("откат не найден — страж смотрит не туда, почините страж")
			}
			if closeAt > rollbackAt {
				t.Fatal("defer res.Close() зарегистрирован ПОСЛЕ отката: номер уйдёт " +
					"в оборот раньше, чем откат снимет созданный интерфейс")
			}
		})
	}
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

// mentions — упоминается ли имя внутри узла. Так опознаётся откат: его
// замыкание обходит стек rollback, и ни один посторонний defer этого не делает.
func mentions(n ast.Node, name string) bool {
	found := false
	ast.Inspect(n, func(x ast.Node) bool {
		if id, ok := x.(*ast.Ident); ok && id.Name == name {
			found = true
		}
		return !found
	})
	return found
}

// funcBody падает сам, если файла или функции нет, — отдельной проверки, что
// страж читает существующий код, не нужно.
func funcBody(t *testing.T, file, fn string) *ast.BlockStmt {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatalf("разобрать %s: %v", file, err)
	}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if ok && fd.Name.Name == fn {
			return fd.Body
		}
	}
	t.Fatalf("%s: функция %s не найдена — страж смотрит не туда", file, fn)
	return nil
}
