package query_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestPointReads_OnlyInsideQuery — точечное чтение интерфейса по имени
// (show interface <name>, show rc interface <name>, резолвер system-name)
// допустимо только в internal/ndms/query: там оно защищено Present и
// выселением по «unable to find» (F546). Литерал в любом другом пакете —
// обход шлюза, и NDMS снова пишет E по отсутствующему имени.
func TestPointReads_OnlyInsideQuery(t *testing.T) {
	bad := []string{
		`.ShowInterface(`,
		`.ShowQuery([]string{"interface"`,
		`"/show/interface/"+`, `"/show/interface/" +`,
		`"/show/rc/interface/"+`, `"/show/rc/interface/" +`,
		`"interface", "system-name"`,
	}
	var offenders []string
	for _, f := range prodGoFiles(t, false) {
		for i, line := range strings.Split(string(f.data), "\n") {
			for _, b := range bad {
				if strings.Contains(line, b) {
					offenders = append(offenders, fmt.Sprintf("%s:%d: %s", f.rel, i+1, b))
				}
			}
		}
	}
	for _, o := range offenders {
		t.Errorf("%s — читать по имени только через query.InterfaceStore (Lookup + ShowRaw)", o)
	}
}

type prodGoFile struct {
	rel  string
	data []byte
}

// prodGoFiles — прод-код репозитория (без _test.go и чужих деревьев);
// withQuery=false пропускает сам internal/ndms/query.
func prodGoFiles(t *testing.T, withQuery bool) []prodGoFile {
	t.Helper()
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatalf("корень репозитория: %v", err)
	}
	var out []prodGoFile
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			switch {
			case rel == "vendor", rel == "frontend", rel == "graphify-out", rel == "docs", rel == ".git",
				rel == ".claude", rel == "build",
				!withQuery && rel == filepath.Join("internal", "ndms", "query"):
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
		out = append(out, prodGoFile{rel: rel, data: data})
		return nil
	})
	if walkErr != nil {
		t.Fatalf("обход дерева: %v", walkErr)
	}
	return out
}

const queryImportPath = "github.com/hoaxisr/awg-manager/internal/ndms/query"

// parseProd разбирает файл и возвращает имя, под которым в нём виден пакет
// query (алиас импорта или "query"); "" — пакет не импортирован.
func parseProd(t *testing.T, f prodGoFile) (*ast.File, *token.FileSet, string) {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, f.rel, f.data, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("разбор %s: %v", f.rel, err)
	}
	for _, imp := range file.Imports {
		if p, _ := strconv.Unquote(imp.Path.Value); p == queryImportPath {
			if imp.Name != nil {
				return file, fset, imp.Name.Name
			}
			return file, fset, "query"
		}
	}
	return file, fset, ""
}

// proofType — имя типа-доказательства (Confirmed/Present), если expr —
// ровно <alias>.Confirmed или <alias>.Present.
func proofType(expr ast.Expr, alias string) string {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	if id, ok := sel.X.(*ast.Ident); !ok || id.Name != alias {
		return ""
	}
	if sel.Sel.Name == "Confirmed" || sel.Sel.Name == "Present" {
		return sel.Sel.Name
	}
	return ""
}

// zeroProofVarExceptions — объявления var x query.Confirmed вне query, где
// нулевое значение не доходит до команды: x присваивается из Confirm/
// RequireIface/снимка до первой команды, а ветка без присваивания команд по
// интерфейсу не шлёт. Ключ — "<путь файла>:<функция>", значение — число
// таких объявлений в функции.
var zeroProofVarExceptions = map[string]int{
	"internal/managed/service_peers.go:AddPeer":       1, // обе ветки присваивают до rciAddPeer
	"internal/managed/service_peers.go:UpdatePeer":    1, // команды — под теми же условиями, что и присваивание
	"internal/managed/service_server.go:Update":       1, // команды — только внутри ветки с присваиванием
	"internal/staticroute/impl.go:applyRoutes":        1, // OS4-ядро: маршрут через ip route, не NDMS
	"internal/staticroute/impl.go:removeRoutes":       1, // то же для снятия
	"internal/tunnel/service/impl.go:syncDescription": 1, // присваивание и команда в одном if
	"internal/tunnel/service/impl.go:applyDiffNWG":    1, // команды — под флагами, которые и вызвали RequireIface
}

// TestProofs_NotForgedOutsideQuery — query.Confirmed и query.Present
// доказывают «запись была в свежем списке NDMS» и рождаются только внутри
// internal/ndms/query (F546, R18). Неэкспортируемые поля не дают вписать
// имя, но нулевое значение компилятор пропускает — с ним команда уйдёт по
// имени "". Вне query запрещены:
//   - литерал query.Confirmed{} / query.Present{} — кроме результата return
//     рядом с ошибкой или ok=false (идиома «значения нет»): return, где все
//     прочие результаты nil/true, — подделка успеха и тоже нарушение;
//   - var x query.Confirmed / query.Present — кроме zeroProofVarExceptions.
//
// Алиас импорта (ndmsquery и др.) учитывается.
func TestProofs_NotForgedOutsideQuery(t *testing.T) {
	seenVars := map[string]int{}
	for _, f := range prodGoFiles(t, false) {
		file, fset, alias := parseProd(t, f)
		if alias == "" {
			continue
		}
		// Литералы, стоящие результатом return рядом с отказом.
		inFailReturn := map[ast.Node]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			ret, ok := n.(*ast.ReturnStmt)
			if !ok {
				return true
			}
			failing := false
			for _, r := range ret.Results {
				if id, ok := r.(*ast.Ident); ok && (id.Name == "nil" || id.Name == "true") {
					continue
				}
				if lit, ok := r.(*ast.CompositeLit); ok && proofType(lit.Type, alias) != "" {
					continue
				}
				failing = true
			}
			if failing {
				for _, r := range ret.Results {
					inFailReturn[r] = true
				}
			}
			return true
		})
		for _, decl := range file.Decls {
			funcName := ""
			if fd, ok := decl.(*ast.FuncDecl); ok {
				funcName = fd.Name.Name
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.CompositeLit:
					if name := proofType(n.Type, alias); name != "" && !inFailReturn[n] {
						t.Errorf("%s: литерал %s.%s{} — доказательство выдаёт только query (Confirm/Lookup)",
							fset.Position(n.Pos()), alias, name)
					}
				case *ast.ValueSpec:
					if n.Type == nil {
						return true
					}
					if name := proofType(n.Type, alias); name != "" {
						key := filepath.ToSlash(f.rel) + ":" + funcName
						seenVars[key]++
						if seenVars[key] > zeroProofVarExceptions[key] {
							t.Errorf("%s: var … %s.%s — нулевое доказательство; получать из query (Confirm/Lookup)",
								fset.Position(n.Pos()), alias, name)
						}
					}
				}
				return true
			})
		}
	}
	for key, want := range zeroProofVarExceptions {
		if seenVars[key] < want {
			t.Errorf("исключение %s: ожидалось %d объявлений, найдено %d — поправить zeroProofVarExceptions", key, want, seenVars[key])
		}
	}
}

// confirmedFieldExceptions — поля с query.Confirmed, которые хранить можно:
// это значения-параметры ОДНОГО вызова, а не долгоживущее состояние.
// Ключ — "<путь файла>:<Тип>.<Поле>".
var confirmedFieldExceptions = map[string]string{
	"internal/ndms/command/wireguard.go:ImportResult.Created":   "результат импорта: вызывающий берёт подтверждение созданного и сразу идёт с ним дальше",
	"internal/ndms/command/routes.go:StaticRouteSpec.Interface": "спека одного вызова Add/RemoveStaticRoute",
	"internal/ndms/command/dnsroutes.go:DNSRouteSpec.Interface": "спека одного вызова ReplaceRoutes",
}

// TestConfirmed_NotStoredInStructs — Confirmed верен на момент чтения списка;
// положенный в поле структуры, он переживает снос интерфейса, и команда по
// нему снова создаст фантом (F546). Поле структуры (в т.ч. *, [] и значение
// map) с типом query.Confirmed — нарушение; параметры функций и локальные
// переменные не задеваются. Сам пакет query тоже проверяется.
func TestConfirmed_NotStoredInStructs(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range prodGoFiles(t, true) {
		file, fset, alias := parseProd(t, f)
		if filepath.Dir(f.rel) == filepath.Join("internal", "ndms", "query") {
			alias = "" // внутри пакета тип виден без квалификатора
		}
		isConfirmed := func(e ast.Expr) bool {
			for {
				switch x := e.(type) {
				case *ast.StarExpr:
					e = x.X
				case *ast.ArrayType:
					e = x.Elt
				case *ast.MapType:
					e = x.Value
				case *ast.Ident:
					return alias == "" && x.Name == "Confirmed"
				default:
					return alias != "" && proofType(e, alias) == "Confirmed"
				}
			}
		}
		if alias == "" && filepath.Dir(f.rel) != filepath.Join("internal", "ndms", "query") {
			continue
		}
		done := map[*ast.StructType]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			typeName := "<анонимная>"
			var st *ast.StructType
			switch n := n.(type) {
			case *ast.TypeSpec:
				s, ok := n.Type.(*ast.StructType)
				if !ok {
					return true
				}
				typeName, st = n.Name.Name, s
			case *ast.StructType:
				st = n
			default:
				return true
			}
			if done[st] {
				return true
			}
			done[st] = true
			for _, fld := range st.Fields.List {
				if !isConfirmed(fld.Type) {
					continue
				}
				names := []string{"<встроенное>"}
				if len(fld.Names) > 0 {
					names = names[:0]
					for _, id := range fld.Names {
						names = append(names, id.Name)
					}
				}
				for _, name := range names {
					key := filepath.ToSlash(f.rel) + ":" + typeName + "." + name
					if seen[key] {
						continue
					}
					seen[key] = true
					if _, ok := confirmedFieldExceptions[key]; ok {
						continue
					}
					t.Errorf("%s: поле %s.%s хранит query.Confirmed — держать подтверждение только в пределах вызова (замыкание/локальная переменная)",
						fset.Position(fld.Pos()), typeName, name)
				}
			}
			return true
		})
	}
	for key := range confirmedFieldExceptions {
		if !seen[key] {
			t.Errorf("исключение %s больше не встречается — убрать из confirmedFieldExceptions", key)
		}
	}
}

// kernelNameCallers — единственное место, где команда по интерфейсу идёт
// по голому имени (F546, R17): на OS4 туннель — имя ядра awgm<N>, записи в
// списке NDMS у него нет, подтверждать нечем.
var kernelNameCallers = map[string]string{
	"internal/tunnel/ops/operator_os4.go": "DNS туннеля OS4 по имени ядра (R17)",
}

// TestKernelNameCommands_OnlyAllowed — Set/ClearDNSByKernelName обходят
// Confirmed; звать их можно только из перечисленных в kernelNameCallers
// файлов. Объявление в internal/ndms/command — не вызов.
func TestKernelNameCommands_OnlyAllowed(t *testing.T) {
	used := map[string]bool{}
	for _, f := range prodGoFiles(t, true) {
		rel := filepath.ToSlash(f.rel)
		for i, line := range strings.Split(string(f.data), "\n") {
			if !strings.Contains(line, "ByKernelName(") || strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if rel == "internal/ndms/command/interfaces.go" &&
				(strings.HasPrefix(line, "func ") || strings.Contains(line, "return c.")) {
				continue // объявления и делегирование SetDNS/ClearDNS
			}
			if _, ok := kernelNameCallers[rel]; ok {
				used[rel] = true
				continue
			}
			t.Errorf("%s:%d: команда по голому имени — только по query.Confirmed (исключения: kernelNameCallers)", rel, i+1)
		}
	}
	for rel := range kernelNameCallers {
		if !used[rel] {
			t.Errorf("исключение %s больше не используется — убрать из kernelNameCallers", rel)
		}
	}
}
