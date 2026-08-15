package awgmproto

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestOnlyStdlibAndXSys — страж переносимости.
//
// Модуль тянут три чужих модуля со своими go.mod и своими наборами версий.
// Любая зависимость сверх стандартной библиотеки и golang.org/x/sys/unix
// (он уже есть у всех трёх форков) приедет к ним в go.mod транзитивно и
// однажды сломает сборку бинаря, а узнаем мы об этом на выкладке, а не здесь.
func TestOnlyStdlibAndXSys(t *testing.T) {
	const allowed = "golang.org/x/sys/unix"

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(".", name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if path == allowed {
				continue
			}
			// Путь без точки в первом сегменте — стандартная библиотека.
			if head, _, _ := strings.Cut(path, "/"); !strings.Contains(head, ".") {
				continue
			}
			t.Fatalf("%s импортирует %q: библиотека обязана обходиться stdlib и %s", name, path, allowed)
		}
	}
}
