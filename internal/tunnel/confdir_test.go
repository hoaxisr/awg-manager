package tunnel

import (
	"path/filepath"
	"testing"
)

// Путь к .conf обязан следовать за каталогом: до этого он собирался из
// захардкоженной строки, а запись шла по отдельной пакетной переменной в двух
// других пакетах. В бою строки совпадали, а в тестах подмена двигала только
// запись — то есть проверялось не то, что происходит на роутере.
func TestNewNamesConfPathFollowsConfDir(t *testing.T) {
	dir := t.TempDir()
	old := ConfDir
	ConfDir = dir
	t.Cleanup(func() { ConfDir = old })

	names := NewNames("awg10")

	want := filepath.Join(dir, "awg10.conf")
	if names.ConfPath != want {
		t.Errorf("ConfPath = %q, want %q", names.ConfPath, want)
	}
}

func TestConfDirDefault(t *testing.T) {
	if ConfDir != "/opt/etc/awg-manager" {
		t.Errorf("ConfDir по умолчанию = %q — на роутере ожидается /opt/etc/awg-manager", ConfDir)
	}
}
