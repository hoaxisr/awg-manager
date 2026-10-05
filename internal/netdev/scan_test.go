package netdev

import (
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// swapImpl — единственный прод-файл, где живут строки снятия/создания
// устройства: реализация Swapper.
const swapImpl = "internal/netdev/swapgate.go"

type srcFile struct {
	rel  string
	data []byte
}

// prodFiles — прод-код (без _test.go) в internal и cmd.
func prodFiles(t *testing.T) []srcFile {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	var out []srcFile
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			out = append(out, srcFile{rel: filepath.ToSlash(rel), data: data})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// inlineLinkCmd — `link del|delete|add` внутри одной строки ("ip link del …").
var inlineLinkCmd = regexp.MustCompile(`\blink\s+(del|delete|add)\b`)

// TestNoBareLinkCommands — страховка к N1 (держит компилятор: примитивы есть
// только у Swapper): ни один прод-файл в internal и cmd, кроме реализации
// Swapper, не собирает команду снятия/создания устройства. Ищется по
// строковым литералам (комментарии не в счёт): "link" с последующим
// литералом del/delete/add (argv в любом форматировании), "tuntap", строка с
// `link del|delete|add` внутри. `link set`/`link show` разрешены. Там же
// запрещён вызов StubRunIP — подмена шва только для тестов.
// Мутация: exec.Run(ctx, ip, "link", "delete", x) в любой прод-файл → красный.
func TestNoBareLinkCommands(t *testing.T) {
	implHits := 0
	for _, f := range prodFiles(t) {
		fset := token.NewFileSet()
		tf := fset.AddFile(f.rel, -1, len(f.data))
		var sc scanner.Scanner
		sc.Init(tf, f.data, nil, 0) // без ScanComments: комментарии пропускаются
		prev := ""                  // предыдущий строковый литерал (через запятые)
		for {
			pos, tok, lit := sc.Scan()
			if tok == token.EOF {
				break
			}
			if tok == token.IDENT && lit == "StubRunIP" && f.rel != swapImpl {
				t.Errorf("%s: StubRunIP в прод-коде — подмена шва только для тестов", fset.Position(pos))
			}
			if tok == token.COMMA {
				continue
			}
			if tok != token.STRING {
				prev = ""
				continue
			}
			v, err := strconv.Unquote(lit)
			if err != nil {
				prev = ""
				continue
			}
			bad := v == "tuntap" || inlineLinkCmd.MatchString(v) ||
				(prev == "link" && (v == "del" || v == "delete" || v == "add"))
			prev = v
			if !bad {
				continue
			}
			if f.rel == swapImpl {
				implHits++
				continue
			}
			t.Errorf("%s: %s — снятие/создание устройства мимо netdev.Swapper (барьер D-N1)", fset.Position(pos), lit)
		}
	}
	if implHits == 0 {
		t.Fatalf("в %s нет ни одной команды — сканер ослеп или реализация переехала", swapImpl)
	}
}

// holdCallerPkgs — где можно звать SwapGate.Hold: fn этих пакетов не читает
// список интерфейсов (иначе Read внутри Hold ждал бы сам себя).
var holdCallerPkgs = map[string]bool{
	"internal/netdev":         true,
	"internal/tunnel/backend": true,
}

// TestHoldCallers_CannotReadList — дедлок «список внутри Hold» исключён по
// построению: Hold зовут только пакеты из holdCallerPkgs, и ни один из них
// (с зависимостями) не импортирует internal/ndms/query — читателя списка.
// Мутации: `.Hold(` из другого пакета → красный; импорт query в backend
// (прямой или транзитивный) → красный.
func TestHoldCallers_CannotReadList(t *testing.T) {
	for _, f := range prodFiles(t) {
		if !strings.Contains(string(f.data), ".Hold(") {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, f.rel, f.data, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Hold" && !holdCallerPkgs[filepath.ToSlash(filepath.Dir(f.rel))] {
				t.Errorf("%s: .Hold( вне %v — fn могла бы читать список под барьером", fset.Position(call.Pos()), holdCallerPkgs)
			}
			return true
		})
	}

	args := []string{"list", "-deps"}
	for pkg := range holdCallerPkgs {
		args = append(args, "github.com/hoaxisr/awg-manager/"+pkg)
	}
	cmd := osexec.Command("go", args...)
	cmd.Dir = "../.."
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if dep == "github.com/hoaxisr/awg-manager/internal/ndms/query" {
			t.Fatalf("%v зависят от %s: fn под Hold может прочитать список — дедлок", holdCallerPkgs, dep)
		}
	}
}
