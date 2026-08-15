package wdtt

import "testing"

// Пин без суммы и размера тихо ломает арку целиком: serverSupported() уже
// true, binariesMatchSpecs() — false, статус вечно зовёт обновляться, а
// установка не доезжает даже до сверки суммы — потолок загрузки Size+1 МиБ
// режет девятимегабайтный сервер. Симптома «не собралось» при этом нет, и
// поймать полупустую секцию можно только здесь.
func TestEmbeddedBinaries_PinsComplete(t *testing.T) {
	for _, arch := range []string{"aarch64-3.10", "mipsel-3.4", "mips-3.4"} {
		specs, ok := EmbeddedBinaries[arch]
		if !ok {
			t.Errorf("%s: нет секции", arch)
			continue
		}
		checkPin(t, arch+"/client", specs.Client, PinnedClientVersion)
		if specs.serverSupported() {
			checkPin(t, arch+"/server", specs.Server, PinnedServerVersion)
		} else if specs.Server.SHA256 != "" || specs.Server.Size != 0 {
			t.Errorf("%s/server: сумма без URL: %+v", arch, specs.Server)
		}
	}
}

func checkPin(t *testing.T, name string, sp BinarySpec, version string) {
	t.Helper()
	if sp.URL == "" || sp.Version != version || len(sp.SHA256) != 64 || sp.Size <= 0 {
		t.Errorf("%s: неполный пин: %+v", name, sp)
	}
}
