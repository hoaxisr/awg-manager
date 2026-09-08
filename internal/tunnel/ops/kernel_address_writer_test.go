package ops

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Адрес на kernel-устройство OS5 кладёт ТОЛЬКО applyKernelAddresses: две
// копии команды в Start и Reconcile уже разъехались (`add` против `replace`),
// и `add` давал EEXIST-WARN на каждом старте после F97. Тест находит вызовы
// ipRun с литералом "address" в operator_os5.go и требует, чтобы все они
// лежали в applyKernelAddresses. OS4 не покрыт: у него свой контракт (awgm-
// устройство без NDMS-адреса) и своя пара configureIP/configureIPv6.
func TestOS5_KernelAddressSingleWriter(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(".", "operator_os5.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var offenders []string
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			for _, a := range call.Args {
				lit, ok := a.(*ast.BasicLit)
				if ok && lit.Kind == token.STRING && lit.Value == `"address"` && fn.Name.Name != "applyKernelAddresses" {
					offenders = append(offenders, fn.Name.Name+" @ "+fset.Position(lit.Pos()).String())
				}
			}
			return true
		})
	}
	if len(offenders) > 0 {
		t.Fatalf("ip address вне applyKernelAddresses:\n%s", strings.Join(offenders, "\n"))
	}
	if _, err := os.Stat("operator_os5.go"); err != nil {
		t.Fatal(err)
	}
}
