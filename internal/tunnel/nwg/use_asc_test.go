package nwg

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// ASC прошивки 5.01 останавливается на AWG 2.0: конфиг 3.x обязан уходить на
// awg_proxy, иначе параметры 3.x не применит никто и туннель встаёт мёртвым.
func TestUseASC(t *testing.T) {
	awg20 := storage.AWGInterface{AWGObfuscation: storage.AWGObfuscation{
		Jc: 4, H1: "10-20", H2: "2", H3: "3", H4: "4",
	}}
	awg31 := storage.AWGInterface{AWGObfuscation: storage.AWGObfuscation{
		Jc: 4, H1: "1", H2: "2", H3: "3", H4: "4",
		HeaderProtectionKey: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		RandomTrailers:      true,
	}}
	awg30 := storage.AWGInterface{AWGObfuscation: storage.AWGObfuscation{
		Jc: 4, H1: "1", H2: "2", H3: "3", H4: "4",
		HeaderProtectionKey: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
	}}

	cases := []struct {
		name       string
		asc, asc3  bool
		iface      storage.AWGInterface
		wantUseASC bool
	}{
		{"прошивка без ASC — всегда прокси", false, false, awg20, false},
		{"ASC 2.0 + конфиг 2.0 — нативно", true, false, awg20, true},
		{"ASC 2.0 + конфиг 3.0 — прокси", true, false, awg30, false},
		{"ASC 2.0 + конфиг 3.1 — прокси", true, false, awg31, false},
		{"ASC 3.x + конфиг 3.1 — нативно", true, true, awg31, true},
		{"ASC 3.x + конфиг 2.0 — нативно", true, true, awg20, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := &OperatorNativeWG{
				supportsASC:  func() bool { return c.asc },
				supportsASC3: func() bool { return c.asc3 },
			}
			if got := o.useASC(&c.iface); got != c.wantUseASC {
				t.Errorf("useASC = %v, want %v", got, c.wantUseASC)
			}
		})
	}
}

// Оператор, собранный без supportsASC3 (тестовые конструкции), обязан считать
// ASC неспособным на 3.x — то есть уходить на прокси, а не паниковать.
func TestUseASCNilASC3IsProxy(t *testing.T) {
	o := &OperatorNativeWG{supportsASC: func() bool { return true }}
	iface := storage.AWGInterface{AWGObfuscation: storage.AWGObfuscation{
		Jc: 4, H1: "1", H2: "2", H3: "3", H4: "4", RandomTrailers: true,
	}}
	if o.useASC(&iface) {
		t.Error("без признака ASC 3.x конфиг 3.1 обязан идти через прокси")
	}
}
