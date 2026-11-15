package config

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestInterfaceKeysDropped(t *testing.T) {
	base := func() *storage.AWGTunnel {
		t := &storage.AWGTunnel{}
		i := &t.Interface
		i.PrivateKey = "k"
		i.Jc, i.Jmin, i.Jmax = 4, 40, 70
		i.H1, i.H2, i.H3, i.H4 = "1", "2", "3", "4"
		i.S3, i.I1, i.I2 = 20, "<b 0x01>", "<b 0x02>"
		i.ContentPaddingAddition = "0-80"
		return t
	}
	for _, tc := range []struct {
		name string
		edit func(*storage.AWGInterface)
		want bool
	}{
		{"без правок", func(*storage.AWGInterface) {}, false},
		{"значение изменено", func(i *storage.AWGInterface) { i.I2 = "<b 0xff>"; i.Jc = 5 }, false},
		{"параметр добавлен", func(i *storage.AWGInterface) { i.I3 = "<b 0x03>" }, false},
		{"I2 убран", func(i *storage.AWGInterface) { i.I2 = "" }, true},
		{"параметр AWG 3.x убран", func(i *storage.AWGInterface) { i.ContentPaddingAddition = "" }, true},
		{"расширенный блок убран целиком", func(i *storage.AWGInterface) { i.S3, i.I1, i.I2 = 0, "", "" }, true},
		{"обфускация убрана целиком", func(i *storage.AWGInterface) { *i = storage.AWGInterface{PrivateKey: "k"} }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			after := base()
			tc.edit(&after.Interface)
			if got := InterfaceKeysDropped(base(), after); got != tc.want {
				t.Fatalf("InterfaceKeysDropped = %v, want %v", got, tc.want)
			}
		})
	}
}
