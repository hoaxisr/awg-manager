package orchestrator

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Инвентарь продьюсеров EventBoot.
//
// `Event{Type: EventBoot}` с незаполненным WANUp молча означает «бута не
// будет»: decideBoot откладывает бут именно по этому полю. Нулевое значение
// bool тут неотличимо от осознанного «WAN лежит», поэтому продьюсер, забывший
// поле, получит тихий no-op вместо загрузки туннелей — то есть F194 заново.
// Компилятор такую забывчивость не ловит, а последствия видны только на
// железе: туннели стоят, и в журнале ни строки.
//
// Тест обходит ВЕСЬ прод-код: любое новое место, конструирующее событие бута,
// обязано явно сказать, поднят WAN или нет.
func TestEventBoot_ProducersAlwaysSetWANUp(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}

	var offenders []string
	var producers int // сколько литералов EventBoot вообще встретилось
	fset := token.NewFileSet()

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "frontend", "build", "vendor":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil // не наше дело чинить чужие синтаксические ошибки
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || !isEventType(lit.Type) {
				return true
			}
			isBoot, hasWANUp := false, false
			for _, el := range lit.Elts {
				kv, ok := el.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok {
					continue
				}
				switch key.Name {
				case "Type":
					if exprName(kv.Value) == "EventBoot" {
						isBoot = true
					}
				case "WANUp":
					hasWANUp = true
				}
			}
			if isBoot {
				producers++
			}
			if isBoot && !hasWANUp {
				rel, _ := filepath.Rel(root, path)
				offenders = append(offenders, rel+":"+
					strconv.Itoa(fset.Position(lit.Pos()).Line))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(offenders) > 0 {
		t.Errorf("EventBoot без явного WANUp (тихо превратится в «бута не будет»): %s",
			strings.Join(offenders, ", "))
	}
	// Нижняя граница. Без неё тест зеленеет, не просмотрев НИЧЕГО: переезд
	// пакета, смена раскладки или сбой разбора исходников (parser.ParseFile
	// здесь молча пропускает файл) превращают страж в декорацию. Проверено
	// подменой корня обхода на пустой каталог — тест проходил.
	if producers == 0 {
		t.Error("не найдено ни одного продьюсера EventBoot — страж ничего не проверил")
	}
}

// isEventType опознаёт и `Event{...}` внутри пакета, и `orchestrator.Event{...}` снаружи.
func isEventType(e ast.Expr) bool {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name == "Event"
	case *ast.SelectorExpr:
		return t.Sel.Name == "Event"
	}
	return false
}

// exprName отдаёт имя идентификатора или поле селектора: EventBoot и
// orchestrator.EventBoot для теста одно и то же.
func exprName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return t.Sel.Name
	}
	return ""
}
