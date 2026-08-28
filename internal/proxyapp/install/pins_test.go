package install

import "testing"

// Пин без суммы и размера тихо ломает арку целиком: serverSupported() уже
// true, binariesMatchSpecs() — false, статус вечно зовёт обновляться, а
// установка не доезжает даже до сверки суммы — потолок загрузки Size+1 МиБ
// режет девятимегабайтный сервер. Симптома «не собралось» при этом нет, и
// поймать полупустую секцию можно только здесь.
func TestWdttEmbeddedBinaries_PinsComplete(t *testing.T) {
	for _, arch := range []string{"aarch64-3.10", "mipsel-3.4", "mips-3.4"} {
		specs, ok := WdttEmbeddedBinaries[arch]
		if !ok {
			t.Errorf("%s: нет секции", arch)
			continue
		}
		checkPin(t, arch+"/client", specs.Client, WdttPinnedClientVersion)
		if specs.serverSupported() {
			checkPin(t, arch+"/server", specs.Server, WdttPinnedServerVersion)
		} else if specs.Server.SHA256 != "" || specs.Server.Size != 0 {
			t.Errorf("%s/server: сумма без URL: %+v", arch, specs.Server)
		}
	}
}

// У freeturn сервер собран под все три арки: полупустая секция здесь — тот же
// дефект, что у wdtt, только без ветки «сервера нет».
func TestFreeTurnEmbeddedBinaries_PinsComplete(t *testing.T) {
	for _, arch := range []string{"aarch64-3.10", "mipsel-3.4", "mips-3.4"} {
		specs, ok := FreeTurnEmbeddedBinaries[arch]
		if !ok {
			t.Errorf("%s: нет секции", arch)
			continue
		}
		checkPin(t, arch+"/client", specs.Client, FreeTurnPinnedVersion)
		checkPin(t, arch+"/server", specs.Server, FreeTurnPinnedVersion)
	}
}

func checkPin(t *testing.T, name string, sp BinarySpec, version string) {
	t.Helper()
	if sp.URL == "" || sp.Version != version || len(sp.SHA256) != 64 || sp.Size <= 0 {
		t.Errorf("%s: неполный пин: %+v", name, sp)
	}
}
