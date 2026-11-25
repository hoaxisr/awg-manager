package testutil

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

// Два приёма, дававших почти все флейки CI. Храповик: накопленное терпим,
// новое — нет.
//
//  1. Listen(":0") → Close → передать номер дальше: номер эфемерный, его успевает
//     взять параллельный тест. Замена — FreePort.
//  2. time.Sleep вне бессчётного цикла опроса и вне пузыря synctest: исход зависит от того,
//     успела ли горутина. Замены — synctest.Test, WaitFor, канал-событие;
//     осознанное окно — с пометкой «sleep-ok: почему» в той же строке.
func TestNoNewFlakePatterns(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	sleeps := map[string]int{}
	fset := token.NewFileSet()
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", "node_modules", ".git", "frontend", "c-core", "kmod":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		lines := strings.Split(string(src), "\n")
		ast.Inspect(f, func(n ast.Node) bool {
			if block, ok := n.(*ast.BlockStmt); ok {
				if v := recycledPort(block.List); v != "" && !recycledPortAllowed[rel] {
					t.Errorf("%s: порт из Listen(\":0\") отдан после %s.Close() — нужен testutil.FreePort", fset.Position(block.Pos()), v)
				}
			}
			return true
		})
		sleeps[filepath.ToSlash(filepath.Dir(rel))] += countSleeps(f, func(p token.Pos) bool {
			return strings.Contains(lines[fset.Position(p).Line-1], "sleep-ok:")
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for dir, n := range sleeps {
		if n > sleepBaseline[dir] {
			t.Errorf("%s: time.Sleep вне цикла и synctest — %d, было %d; нужен synctest.Test, testutil.WaitFor или канал", dir, n, sleepBaseline[dir])
		}
	}
}

func isSel(e ast.Expr, pkg, name string) bool {
	s, ok := e.(*ast.SelectorExpr)
	if !ok || !strings.HasPrefix(s.Sel.Name, name) {
		return false
	}
	x, ok := s.X.(*ast.Ident)
	return ok && x.Name == pkg
}

func isPollOrBubble(n ast.Node) bool {
	switch n := n.(type) {
	case *ast.ForStmt:
		return n.Post == nil // счётный цикл — тот же срок: N проб по M мс
	case *ast.CallExpr:
		return isSel(n.Fun, "synctest", "Test")
	}
	return false
}

func countSleeps(f *ast.File, allowed func(token.Pos) bool) int {
	n := 0
	ast.Inspect(f, func(node ast.Node) bool {
		if isPollOrBubble(node) {
			return false
		}
		if c, ok := node.(*ast.CallExpr); ok && isSel(c.Fun, "time", "Sleep") && !allowed(c.Pos()) {
			n++
		}
		return true
	})
	return n
}

// recycledPort — в списке операторов есть X := net.Listen(…:0) и дальше, без
// другого употребления X кроме X.Addr(), немедленный X.Close().
func recycledPort(list []ast.Stmt) string {
	for i, st := range list {
		as, ok := st.(*ast.AssignStmt)
		if !ok || len(as.Rhs) != 1 {
			continue
		}
		call, ok := as.Rhs[0].(*ast.CallExpr)
		if !ok || !(isSel(call.Fun, "net", "Listen") || isSel(call.Fun, "tls", "Listen")) || len(call.Args) < 2 {
			continue
		}
		lit, ok := call.Args[1].(*ast.BasicLit)
		id, ok2 := as.Lhs[0].(*ast.Ident)
		if !ok || !ok2 || !strings.HasSuffix(lit.Value, `:0"`) {
			continue
		}
		for _, next := range list[i+1:] {
			if ifs, ok := next.(*ast.IfStmt); ok && ifs.Init == nil {
				continue // if err != nil { t.Fatal }
			}
			closed, other := usesOf(next, id.Name)
			if closed {
				return id.Name
			}
			if other {
				break
			}
		}
	}
	return ""
}

func usesOf(st ast.Stmt, name string) (closed, other bool) {
	if _, ok := st.(*ast.DeferStmt); ok {
		return false, true
	}
	addr := map[*ast.Ident]bool{}
	ast.Inspect(st, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncLit:
			ast.Inspect(n, func(m ast.Node) bool {
				if id, ok := m.(*ast.Ident); ok && id.Name == name {
					other = true
				}
				return true
			})
			return false
		case *ast.SelectorExpr:
			if id, ok := n.X.(*ast.Ident); ok && id.Name == name {
				switch n.Sel.Name {
				case "Close":
					closed = true
				case "Addr":
					addr[id] = true
				default:
					other = true
				}
			}
		case *ast.Ident:
			if n.Name == name && !addr[n] && !closed {
				other = true
			}
		}
		return true
	})
	return closed, other
}

// Приёмка на роутере: запускается руками, параллельных тестов там нет.
var recycledPortAllowed = map[string]bool{
	"internal/firc/uplink_router_acceptance_linux_test.go":      true,
	"internal/singbox/subscription/router_uplink_linux_test.go": true,
}

// Сколько снов лежало на момент появления проверки. Число только уменьшать.
var sleepBaseline = map[string]int{
	"awgmautolist/internal/awgm":         1,
	"awgmautolist/internal/capture":      1,
	"awgmautolist/internal/engine":       12,
	"awgmautolist/internal/probe":        1,
	"awgmproto":                          4,
	"internal/api":                       8,
	"internal/auth":                      1,
	"internal/autolistdetect":            2,
	"internal/cdndispatcher":             1,
	"internal/deviceproxy":               1,
	"internal/dnscheck":                  1,
	"internal/downloader":                1,
	"internal/firc":                      14,
	"internal/fpclient":                  1,
	"internal/managed":                   1,
	"internal/mcp":                       1,
	"internal/monitoring":                1,
	"internal/ndms/cache":                5,
	"internal/ndms/command":              3,
	"internal/ndms/events":               7,
	"internal/ndms/query":                16,
	"internal/ndms/transport":            8,
	"internal/netdev":                    1,
	"internal/obfuscator":                1,
	"internal/opkgtun":                   1,
	"internal/orchestrator":              2,
	"internal/pingcheck":                 1,
	"internal/proxyrt":                   4,
	"internal/proxyrt/control":           1,
	"internal/proxyrt/roles/procres":     3,
	"internal/realityserver":             3,
	"internal/serverwizard":              1,
	"internal/singbox":                   21,
	"internal/singbox/awgoutbounds":      1,
	"internal/singbox/installer":         1,
	"internal/singbox/orchestrator":      9,
	"internal/singbox/router":            18,
	"internal/singbox/subscription":      2,
	"internal/sys/httpclient":            1,
	"internal/sys/ndmsinfo":              1,
	"internal/sys/procmon":               1,
	"internal/tgwebproxy/tproxy/server":  2,
	"internal/tgwebproxy/tproxy/session": 1,
	"internal/tunnel/backend":            2,
	"internal/tunnel/nwg":                2,
	"internal/tunnel/ops":                2,
	"internal/uplink/systemwg":           1,
	"internal/uplink/wireguard":          8,
	"internal/warpscout":                 5,
	"internal/xhttpserver":               14,
}
