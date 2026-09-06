package install

import (
	"context"
	"testing"
)

func TestObfSubsystems_SingleBinary(t *testing.T) {
	s := New(Deps{Arch: "mipsel-3.4"})
	for _, name := range []string{"obf-phobos", "obf-clusterm"} {
		sub, err := s.pick(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if sub.specs == nil || sub.specs.serverSupported() {
			t.Fatalf("%s: specs=%+v — ожидается один клиентский бинарь", name, sub.specs)
		}
		if sub.clientBin != "/opt/bin/awgm-wg-obfuscator-"+name[len("obf-"):] {
			t.Fatalf("%s: bin %s", name, sub.clientBin)
		}
	}
}

func TestEnsureInstalled_UnknownSubsystem(t *testing.T) {
	if _, err := New(Deps{Arch: "mipsel-3.4"}).EnsureInstalled(context.Background(), "nope"); err == nil {
		t.Fatal("expected error")
	}
}
