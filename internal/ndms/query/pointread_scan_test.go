package query_test

import (
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// byNamePatterns — формы запроса интерфейса ПО ИМЕНИ (F546, решение владельца
// 30.09 «убрать все чтения по имени»): на снятом интерфейсе NDMS пишет E
// «unable to find». Ищутся в коде без комментариев.
var byNamePatterns = []string{
	`.ShowInterface(`, // хелпер удалён; шаблон — от воскрешения
	`"/show/interface/"+`, `"/show/interface/" +`, `"/show/interface/%`, `"/show/interface"`,
	`"/show/rc/interface/"+`, `"/show/rc/interface/" +`, `"/show/rc/interface/%`, `"/show/rc/interface"`,
	`Present{`, `Interfaces.Lookup(ctx`, `interfaces.Lookup(ctx`,
}

// byNameAllowed — где форма по имени допустима. Ключ — "<путь файла>:<функция>".
var byNameAllowed = map[string]string{
	// Остаток F570 (§3.4): пакетный резолвер system-name вслед за свежим
	// списком — единственное чтение с именем в теле; под TestResolver_OnlyAfterList.
	"internal/ndms/query/interfaces.go:resolveSystemNames": "F570: пакет system-name вслед за списком",
	"internal/ndms/query/interfaces.go:fetchSystemName":    "F570: элемент того же пакета",
}

// fullListLiteralAllowed — где может стоять голый литерал пути полного списка
// ("/show/interface/", "/show/rc/interface/"): чтение ЦЕЛОГО списка/дерева и
// разбор пути в транспорте и оракуле. В любом другом месте литерал — заготовка
// пути по имени (path.Join("/show/interface/", x), const p = …; p+x).
var fullListLiteralAllowed = map[string]string{
	"internal/ndms/query/interfaces.go:fetchListMap": "GET /show/interface/ — весь список",
	"internal/ndms/query/rcinterfaces.go:fetch":      "GET /show/rc/interface/ — всё дерево rc",
	"internal/ndms/query/getter.go:rcTreeLocked":     "FakeGetter: ответ на чтение дерева",
	"internal/ndms/query/fakendms.go:GetRaw":         "оракул: разбор пути запроса",
	"internal/ndms/transport/client.go:bypassBatch":  "транспорт: дерево rc мимо батчера (префикс)",
}

// TestByNameReads_Absent — ни одного запроса интерфейса по имени во ВСЁМ
// прод-коде, включая internal/ndms/query (F546): ни `show interface name=X`
// (POST-форма ShowQuery), ни GET `/show/interface/X`, ни `/show/rc/interface/X`.
// Голые литералы путей полного списка — только в fullListLiteralAllowed.
// Комментарии не в счёт (go/scanner). ShowQuery с первым элементом пути
// "interface" и аргументами — только в byNameAllowed и только с "system-name"
// вторым элементом. Оракул FakeNDMS точечные чтения моделирует, но сам
// их не шлёт — шаблонов в нём нет.
func TestByNameReads_Absent(t *testing.T) {
	used, usedFull := map[string]bool{}, map[string]bool{}
	for _, f := range prodGoFiles(t, true) {
		rel := filepath.ToSlash(f.rel)
		code := withoutComments(t, f)
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, f.rel, f.data, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("разбор %s: %v", f.rel, err)
		}
		funcAt := func(off int) string {
			for _, d := range file.Decls {
				if fd, ok := d.(*ast.FuncDecl); ok && fset.Position(fd.Pos()).Offset <= off && off < fset.Position(fd.End()).Offset {
					return fd.Name.Name
				}
			}
			return ""
		}
		report := func(off int, what string) {
			key := rel + ":" + funcAt(off)
			if _, ok := byNameAllowed[key]; ok {
				used[key] = true
				return
			}
			line := 1 + strings.Count(string(f.data[:off]), "\n")
			t.Errorf("%s:%d: %s — запрос интерфейса по имени (F546); данные — из снимка списка или дерева rc", rel, line, what)
		}
		for _, pat := range byNamePatterns {
			for from := 0; ; {
				k := strings.Index(code[from:], pat)
				if k < 0 {
					break
				}
				report(from+k, pat)
				from += k + len(pat)
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if bl, ok := n.(*ast.BasicLit); ok && (isStringLit(bl, "/show/interface/") || isStringLit(bl, "/show/rc/interface/")) {
				key := rel + ":" + funcAt(fset.Position(bl.Pos()).Offset)
				if _, ok := fullListLiteralAllowed[key]; ok {
					usedFull[key] = true
				} else {
					t.Errorf("%s: литерал %s вне чтения полного списка — заготовка пути по имени (F546)", fset.Position(bl.Pos()), bl.Value)
				}
				return true
			}
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "ShowQuery" {
				return true
			}
			lit, ok := call.Args[0].(*ast.CompositeLit)
			if !ok || len(lit.Elts) == 0 || !isStringLit(lit.Elts[0], "interface") {
				return true
			}
			if len(lit.Elts) > 1 && isStringLit(lit.Elts[1], "system-name") {
				report(fset.Position(call.Pos()).Offset, "ShowQuery interface system-name")
				return true
			}
			if len(call.Args) > 1 && !isNilIdent(call.Args[1]) {
				// Разрешено только в пакете резолвера (выше) — здесь форма по имени.
				t.Errorf("%s: ShowQuery([]string{\"interface\"…}, аргументы) — запрос интерфейса по имени (F546)",
					fset.Position(call.Pos()))
			}
			return true
		})
	}
	for key := range byNameAllowed {
		if !used[key] {
			t.Errorf("исключение %s больше не встречается — убрать из byNameAllowed", key)
		}
	}
	for key := range fullListLiteralAllowed {
		if !usedFull[key] {
			t.Errorf("исключение %s больше не встречается — убрать из fullListLiteralAllowed", key)
		}
	}
}

// withoutComments — исходник, где комментарии заменены пробелами (смещения
// те же): литерал в комментарии не запрос.
func withoutComments(t *testing.T, f prodGoFile) string {
	t.Helper()
	out := []byte(string(f.data))
	fset := token.NewFileSet()
	tf := fset.AddFile(f.rel, -1, len(f.data))
	var sc scanner.Scanner
	sc.Init(tf, f.data, func(pos token.Position, msg string) { t.Fatalf("%s: %s", pos, msg) }, scanner.ScanComments)
	for {
		pos, tok, lit := sc.Scan()
		if tok == token.EOF {
			break
		}
		if tok == token.COMMENT {
			off := tf.Offset(pos)
			for i := off; i < off+len(lit); i++ {
				if out[i] != '\n' {
					out[i] = ' '
				}
			}
		}
	}
	return string(out)
}

func isStringLit(e ast.Expr, want string) bool {
	bl, ok := e.(*ast.BasicLit)
	if !ok || bl.Kind != token.STRING {
		return false
	}
	v, err := strconv.Unquote(bl.Value)
	return err == nil && v == want
}

func isNilIdent(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "nil"
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

// proofType — имя типа-доказательства (Confirmed), если expr — ровно
// <alias>.Confirmed.
func proofType(expr ast.Expr, alias string) string {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	if id, ok := sel.X.(*ast.Ident); !ok || id.Name != alias {
		return ""
	}
	if sel.Sel.Name == "Confirmed" {
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
	"internal/tunnel/service/impl.go:ReplaceConfig":   1, // команды — в той же nwg-ветке и только при nwgIfaceErr == nil
}

// TestProofs_NotForgedOutsideQuery — query.Confirmed доказывает «запись была
// в свежем списке NDMS» и рождается только внутри
// internal/ndms/query (F546, R18). Неэкспортируемые поля не дают вписать
// имя, но нулевое значение компилятор пропускает — с ним команда уйдёт по
// имени "". Вне query запрещены:
//   - литерал query.Confirmed{} — кроме результата return
//     рядом с ошибкой или ok=false (идиома «значения нет»): return с false
//     среди результатов или с последним результатом не nil; иначе
//     (`…, nil`, `…, true, nil`, `…, "", nil`) — подделка успеха;
//   - var x query.Confirmed — кроме zeroProofVarExceptions.
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
			// Отказ: среди результатов есть false (ok=false) или последний
			// результат — не nil (ошибка). `return query.Confirmed{}, "", nil`
			// — подделка успеха.
			failing := false
			for _, r := range ret.Results {
				if id, ok := r.(*ast.Ident); ok && id.Name == "false" {
					failing = true
				}
			}
			if n := len(ret.Results); n > 0 {
				if id, ok := ret.Results[n-1].(*ast.Ident); !ok || id.Name != "nil" {
					failing = true
				}
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
						t.Errorf("%s: литерал %s.%s{} — доказательство выдаёт только query (Confirm)",
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
							t.Errorf("%s: var … %s.%s — нулевое доказательство; получать из query (Confirm)",
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

const netdevImportPath = "github.com/hoaxisr/awg-manager/internal/netdev"

// TestScanner_NetdevFreeNotForged — netdev.Free доказывает «устройства нет»
// и рождается только в netdev.Absent (F569). Вне internal/netdev запрещены и
// литерал netdev.Free{…}, и var x netdev.Free: нулевое значение — имя "",
// исключений нет (в отличие от Confirmed, отказу Free не нужен вовсе —
// вызывающий возвращает только ошибку). Алиас импорта учитывается.
func TestScanner_NetdevFreeNotForged(t *testing.T) {
	for _, f := range prodGoFiles(t, true) {
		if filepath.Dir(f.rel) == filepath.Join("internal", "netdev") ||
			!strings.Contains(string(f.data), netdevImportPath) {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, f.rel, f.data, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("разбор %s: %v", f.rel, err)
		}
		alias := ""
		for _, imp := range file.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == netdevImportPath {
				alias = "netdev"
				if imp.Name != nil {
					alias = imp.Name.Name
				}
			}
		}
		isFree := func(e ast.Expr) bool {
			sel, ok := e.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Free" {
				return false
			}
			id, ok := sel.X.(*ast.Ident)
			return ok && id.Name == alias
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.CompositeLit:
				if isFree(n.Type) {
					t.Errorf("%s: литерал %s.Free{} — доказательство выдаёт только netdev.Absent", fset.Position(n.Pos()), alias)
				}
			case *ast.ValueSpec:
				if n.Type != nil && isFree(n.Type) {
					t.Errorf("%s: var … %s.Free — нулевое доказательство; получать из netdev.Absent", fset.Position(n.Pos()), alias)
				}
			}
			return true
		})
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

// kernelNameCallers — где можно обращаться к Set/ClearDNSByKernelName
// (команда по голому имени, F546, R17). Ключ — "<путь файла>:<функция>".
var kernelNameCallers = map[string]string{
	"internal/tunnel/ops/operator_os4.go:dnsByKernelName": "DNS туннеля OS4 по имени ядра awgm<N>: записи в списке NDMS нет, подтверждать нечем",
	"internal/ndms/command/interfaces.go:SetDNS":          "Confirmed-версия делегирует в строковое тело",
	"internal/ndms/command/interfaces.go:ClearDNS":        "то же для снятия",
}

// TestKernelNameCommands_OnlyAllowed — Set/ClearDNSByKernelName обходят
// Confirmed; любое обращение к ним (вызов или значение метода) вне функций
// из kernelNameCallers — нарушение. Объявления обращением не считаются.
func TestKernelNameCommands_OnlyAllowed(t *testing.T) {
	used := map[string]bool{}
	for _, f := range prodGoFiles(t, true) {
		if !strings.Contains(string(f.data), "ByKernelName") {
			continue
		}
		file, fset, _ := parseProd(t, f)
		for _, decl := range file.Decls {
			key := filepath.ToSlash(f.rel) + ":"
			if fd, ok := decl.(*ast.FuncDecl); ok {
				key += fd.Name.Name
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || (sel.Sel.Name != "SetDNSByKernelName" && sel.Sel.Name != "ClearDNSByKernelName") {
					return true
				}
				if _, ok := kernelNameCallers[key]; ok {
					used[key] = true
					return true
				}
				t.Errorf("%s: %s — команда по голому имени, только по query.Confirmed (исключения: kernelNameCallers)",
					fset.Position(sel.Pos()), sel.Sel.Name)
				return true
			})
		}
	}
	for key := range kernelNameCallers {
		if !used[key] {
			t.Errorf("исключение %s больше не используется — убрать из kernelNameCallers", key)
		}
	}
}

// notListedSite — единственное место вне query, где ветвятся по
// query.ErrNotListed: там исключение F584 — снос `no interface X` по имени из
// нашей же принятой команды создания через query.Unlisted (см.
// command/mutator.go). Ключ — файл:функция без ресивера.
const notListedSite = "internal/ndms/command/mutator.go:confirmCreated"

// TestNotListed_OnlyInConfirmCreated — обращение к ErrNotListed в прод-коде
// вне query — только в notListedSite: иначе второе место могло бы слать
// команды по неподтверждённому имени.
func TestNotListed_OnlyInConfirmCreated(t *testing.T) {
	used := false
	for _, f := range prodGoFiles(t, false) {
		file, fset, alias := parseProd(t, f)
		if alias == "" {
			continue
		}
		for _, decl := range file.Decls {
			key := filepath.ToSlash(f.rel) + ":"
			if fd, ok := decl.(*ast.FuncDecl); ok {
				key += fd.Name.Name
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "ErrNotListed" {
					return true
				}
				if id, ok := sel.X.(*ast.Ident); !ok || id.Name != alias {
					return true
				}
				if key == notListedSite {
					used = true
					return true
				}
				t.Errorf("%s: %s.ErrNotListed вне %s — снос по неподтверждённому имени только там (F584)",
					fset.Position(sel.Pos()), alias, notListedSite)
				return true
			})
		}
	}
	if !used {
		t.Errorf("%s больше не обращается к ErrNotListed — поправить notListedSite", notListedSite)
	}
}

// TestRawInterfaceDelete_OnlyConfirmed — снос интерфейса литералом
// ({"interface":{X:{"no":true}}} или {"interface":{"name":X,"no":true}}) в
// прод-коде — ровно один, в deleteSite, и только по имени из Confirmed: X —
// вызов `….Name()` или переменная, присвоенная из него в той же функции.
// Только там выдаётся кредит своего ifdestroyed (ExpectRemoval): второй
// композит сноса, даже по Confirmed, снимал бы запись без кредита, и её
// ifdestroyed стал бы чужим (R67-1). Снос неподтверждённого (F584) идёт через
// query.Unlisted в тот же deleteInterface. Формы `{"parse":"no interface X"}`
// сносом здесь не бывают (снимают настройку) и не проверяются.
// Мутация: второй композит `{"interface":{iface.Name():{"no":true}}}` в
// command/proxy.go → красный.
func TestRawInterfaceDelete_OnlyConfirmed(t *testing.T) {
	const deleteSite = "internal/ndms/command/delete.go:deleteInterface"
	sites := 0
	for _, f := range prodGoFiles(t, true) {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, f.rel, f.data, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("разбор %s: %v", f.rel, err)
		}
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			key := filepath.ToSlash(f.rel) + ":" + fd.Name.Name
			fromName := map[string]bool{} // переменные := x.Name()
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if as, ok := n.(*ast.AssignStmt); ok && len(as.Lhs) == len(as.Rhs) {
					for i, r := range as.Rhs {
						if id, ok := as.Lhs[i].(*ast.Ident); ok && isNameCall(r) {
							fromName[id.Name] = true
						}
					}
				}
				return true
			})
			ok2 := func(e ast.Expr) bool {
				if isNameCall(e) {
					return true
				}
				id, ok := e.(*ast.Ident)
				return ok && fromName[id.Name]
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				kv, ok := n.(*ast.KeyValueExpr)
				if !ok || !isStringLit(kv.Key, "interface") {
					return true
				}
				inner, ok := kv.Value.(*ast.CompositeLit)
				if !ok {
					return true
				}
				var names []ast.Expr
				if hasNoTrue(inner) { // payloads-форма {"name":X,"no":true}
					for _, el := range inner.Elts {
						if e, ok := el.(*ast.KeyValueExpr); ok && isStringLit(e.Key, "name") {
							names = append(names, e.Value)
						}
					}
				}
				for _, el := range inner.Elts { // command-форма {X:{"no":true}}
					e, ok := el.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					if body, ok := e.Value.(*ast.CompositeLit); ok && hasNoTrue(body) {
						names = append(names, e.Key)
					}
				}
				for _, x := range names {
					sites++
					if key != deleteSite {
						t.Errorf("%s: %s: снос интерфейса вне %s — без кредита своего ifdestroyed",
							fset.Position(x.Pos()), key, deleteSite)
					}
					if !ok2(x) {
						t.Errorf("%s: %s: снос интерфейса по имени не из Confirmed — только по query.Confirmed",
							fset.Position(x.Pos()), key)
					}
				}
				return true
			})
		}
	}
	if sites != 1 {
		t.Errorf("композитов сноса интерфейса %d, ждали ровно один (%s) — сканер не видит формы", sites, deleteSite)
	}
}

// prodCalls — каждый вызов `….name(…)` или `name(…)` в прод-коде internal/ и
// cmd/ (без комментариев и объявлений) с ключом "файл:функция".
func prodCalls(t *testing.T, name string) (keys []string) {
	t.Helper()
	for _, f := range prodGoFiles(t, true) {
		rel := filepath.ToSlash(f.rel)
		if !strings.HasPrefix(rel, "internal/") && !strings.HasPrefix(rel, "cmd/") {
			continue
		}
		if !strings.Contains(string(f.data), name) {
			continue
		}
		file, fset, _ := parseProd(t, f)
		for _, decl := range file.Decls {
			key := rel + ":"
			if fd, ok := decl.(*ast.FuncDecl); ok {
				key += fd.Name.Name
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fn := call.Fun.(type) {
				case *ast.SelectorExpr:
					ok = fn.Sel.Name == name
				case *ast.Ident:
					ok = fn.Name == name
				default:
					ok = false
				}
				if ok {
					keys = append(keys, key+" @ "+fset.Position(call.Pos()).String())
				}
				return true
			})
		}
	}
	return keys
}

// TestExpectRemoval_OnlyInDeleteInterface — кредит снятия выдаёт только
// единственный путь `no interface` (П24): второй вызывающий выдавал бы кредит
// без нашей команды или слал бы `no` мимо жетона.
// Мутация: второй ExpectRemoval( в прод-файле → красный.
func TestExpectRemoval_OnlyInDeleteInterface(t *testing.T) {
	const site = "internal/ndms/command/delete.go:deleteInterface"
	calls := prodCalls(t, "ExpectRemoval")
	if len(calls) != 1 || !strings.HasPrefix(calls[0], site+" @ ") {
		t.Errorf("ExpectRemoval вызывается %v, ждали ровно один раз в %s", calls, site)
	}
}

// TestUnlisted_OnlyInNotListedSite — Confirmed без списка получает только
// снос записи, доказанно созданной и не показанной (F584).
// Мутация: второй вызов Unlisted( в прод-файле → красный.
func TestUnlisted_OnlyInNotListedSite(t *testing.T) {
	calls := prodCalls(t, "Unlisted")
	if len(calls) != 1 || !strings.HasPrefix(calls[0], notListedSite+" @ ") {
		t.Errorf("query.Unlisted вызывается %v, ждали ровно один раз в %s", calls, notListedSite)
	}
}

// TestNoInterfaceForgetOutsideStore — карту при нашем снятии чистит только
// RemovalToken, `no interface` — только deleteInterface: ни Forget, ни
// payload-форм сноса и сохранения мимо них в прод-коде нет (П24).
// Мутация: вернуть payloads.CmdSave или Interfaces.Forget( → красный.
func TestNoInterfaceForgetOutsideStore(t *testing.T) {
	for _, f := range prodGoFiles(t, true) {
		code := withoutComments(t, f)
		for _, bad := range []string{"Interfaces.Forget(", "CmdInterfaceDelete", "CmdSave"} {
			if strings.Contains(code, bad) {
				t.Errorf("%s: %s в прод-коде (П24)", f.rel, bad)
			}
		}
	}
}

// TestSavePayload_OnlyInCoordinator — `system configuration save` в прод-коде
// шлёт только SaveCoordinator: сохранение мимо него — полёт, которого
// координатор не видит (П24, В3). Ищется литерал "save" под ключом
// "configuration" и строка `configuration save` (форма parse).
// Мутация: вернуть save в батч nwg → красный.
func TestSavePayload_OnlyInCoordinator(t *testing.T) {
	const site = "internal/ndms/command/save.go"
	used := false
	for _, f := range prodGoFiles(t, true) {
		rel := filepath.ToSlash(f.rel)
		file, fset, _ := parseProd(t, f)
		ast.Inspect(file, func(n ast.Node) bool {
			hit := false
			switch n := n.(type) {
			case *ast.KeyValueExpr:
				if inner, ok := n.Value.(*ast.CompositeLit); ok && isStringLit(n.Key, "configuration") {
					for _, el := range inner.Elts {
						if kv, ok := el.(*ast.KeyValueExpr); ok && isStringLit(kv.Key, "save") {
							hit = true
						}
					}
				}
			case *ast.BasicLit:
				hit = n.Kind == token.STRING && strings.Contains(n.Value, "configuration save")
			}
			if !hit {
				return true
			}
			if rel == site {
				used = true
				return true
			}
			t.Errorf("%s: сохранение конфигурации мимо SaveCoordinator (только %s)", fset.Position(n.Pos()), site)
			return true
		})
	}
	if !used {
		t.Errorf("%s больше не шлёт save — поправить сканер", site)
	}
}

// isNameCall — e есть вызов `….Name()` без аргументов.
func isNameCall(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Name"
}

// hasNoTrue — в литерале есть "no": true.
func hasNoTrue(cl *ast.CompositeLit) bool {
	for _, el := range cl.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok || !isStringLit(kv.Key, "no") {
			continue
		}
		if id, ok := kv.Value.(*ast.Ident); ok && id.Name == "true" {
			return true
		}
	}
	return false
}

// TestInterfaceStore_NoHookOwnedExistence — существование записи решает
// список, а не доставка хуков (F595): в InterfaceStore нет полей-владельцев
// «ждущего» существования, а оракул не умеет прятать запись до хука.
// Мутация: поле `pending map[string]struct{}` в структуру → красный.
func TestInterfaceStore_NoHookOwnedExistence(t *testing.T) {
	typ := reflect.TypeOf(query.InterfaceStore{})
	for _, name := range []string{"pending", "tombs", "awaiting", "touched"} {
		if _, ok := typ.FieldByName(name); ok {
			t.Errorf("InterfaceStore.%s: запись в списке не зависит от хуков (F595)", name)
		}
	}
	defer func() {
		if recover() == nil {
			t.Fatal("HideCreated(-1) должен паниковать")
		}
	}()
	query.NewFakeNDMS().HideCreated(-1)
}

// listPathSite — единственное прод-чтение полного списка интерфейсов: под
// барьером netdev.SwapGate (D-N1). Второй читатель списка шёл бы мимо барьера
// и в зазоре подмены устройства давал бы C 0767.
const listPathSite = "internal/ndms/query/interfaces.go:fetchListMap"

// TestInterfaceListPath_OnlyInFetchListMap — литерал пути списка
// ("/show/interface/" и "/show/interface") в прод-коде — только в
// listPathSite; POST-форма того же чтения (ShowQuery([]string{"interface"},
// …)) — нигде. Исключения: комментарии (go/ast их литералами не видит) и
// оракул query/fakendms.go, который путь разбирает, а не читает (N9).
// Мутация: второй getter.Get(ctx, "/show/interface/", …) в прод-коде → красный.
func TestInterfaceListPath_OnlyInFetchListMap(t *testing.T) {
	used := false
	for _, f := range prodGoFiles(t, true) {
		rel := filepath.ToSlash(f.rel)
		if rel == "internal/ndms/query/fakendms.go" {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, f.rel, f.data, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("разбор %s: %v", f.rel, err)
		}
		for _, decl := range file.Decls {
			key := rel + ":"
			if fd, ok := decl.(*ast.FuncDecl); ok {
				key += fd.Name.Name
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.BasicLit:
					if !isStringLit(n, "/show/interface/") && !isStringLit(n, "/show/interface") {
						return true
					}
					if key == listPathSite {
						used = true
						return true
					}
					t.Errorf("%s: %s — чтение списка интерфейсов мимо барьера (только %s)", fset.Position(n.Pos()), n.Value, listPathSite)
				case *ast.CallExpr:
					sel, ok := n.Fun.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "ShowQuery" || len(n.Args) == 0 {
						return true
					}
					if lit, ok := n.Args[0].(*ast.CompositeLit); ok && len(lit.Elts) == 1 && isStringLit(lit.Elts[0], "interface") {
						t.Errorf("%s: ShowQuery([]string{\"interface\"}, …) — чтение списка мимо барьера (только %s)", fset.Position(n.Pos()), listPathSite)
					}
				}
				return true
			})
		}
	}
	if !used {
		t.Errorf("%s больше не читает список — поправить listPathSite", listPathSite)
	}
}
