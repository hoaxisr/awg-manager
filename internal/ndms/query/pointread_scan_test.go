package query_test

import (
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
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
	// Полный список своим клиентом (не по имени; F-n — мимо InterfaceStore).
	"internal/sys/routerinfo/routerinfo.go:fetchWiFiTemps": "GET /show/interface — весь список, без имени",
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
