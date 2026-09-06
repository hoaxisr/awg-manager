package obfuscator

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestRenderConf(t *testing.T) {
	o := &storage.Obfuscator{Flavor: "phobos", Target: "h:1", Key: "k", Masking: "MEDIA", MaxDummy: 4, IdleTimeout: 60, ObfuscateBytes: 16, LocalPort: 39001}
	got := RenderConf(o)
	for _, want := range []string{"[main]", "source-if = 127.0.0.1", "source-lport = 39001", "target = h:1", "key = k", "masking = MEDIA", "max-dummy = 4", "idle-timeout = 60", "obfuscate-bytes = 16", "verbose = INFO"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	c := RenderConf(&storage.Obfuscator{Flavor: "clusterm", Target: "h:1", Key: "k", Masking: "STUN", LocalPort: 39002})
	if strings.Contains(c, "obfuscate-bytes") || strings.Contains(c, "idle-timeout") {
		t.Fatalf("clusterm/zero fields leaked:\n%s", c)
	}
}

// setTestDirs подменяет ConfDir/RunDir на время теста и возвращает их.
func setTestDirs(t *testing.T) {
	t.Helper()
	oc, orun := ConfDir, RunDir
	ConfDir, RunDir = t.TempDir(), t.TempDir()
	t.Cleanup(func() { ConfDir, RunDir = oc, orun })
}

func TestWriteRemoveConf(t *testing.T) {
	setTestDirs(t)
	o := &storage.Obfuscator{Flavor: "phobos", Target: "h:1", Key: "k", Masking: "STUN", LocalPort: 39001}
	if err := WriteConf("awg20", o); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(ConfDir, "awg20.conf"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("stat=%v mode=%v", err, st.Mode())
	}
	RemoveConf("awg20")
	if _, err := os.Stat(ConfPath("awg20")); !os.IsNotExist(err) {
		t.Fatal("not removed")
	}
	RemoveConf("awg20") // идемпотентно
}

func TestPickLocalPort(t *testing.T) {
	p, err := PickLocalPort(func(int) bool { return false })
	if err != nil || p < PortMin || p > PortMax {
		t.Fatalf("p=%d err=%v", p, err)
	}
	// Занятый в сторе — пропускаем.
	p2, _ := PickLocalPort(func(x int) bool { return x == p })
	if p2 == p {
		t.Fatal("taken port reused")
	}
	// Занятый чужим процессом на loopback — пропускаем.
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: p})
	if err != nil {
		t.Skip("cannot bind loopback in this sandbox")
	}
	defer c.Close()
	if PortFree(p) {
		t.Fatal("bound port reported free")
	}
	p3, _ := PickLocalPort(func(int) bool { return false })
	if p3 == p {
		t.Fatal("bound port picked")
	}
	_, err = PickLocalPort(func(int) bool { return true })
	if err == nil {
		t.Fatal("exhausted pool must error")
	}
}
