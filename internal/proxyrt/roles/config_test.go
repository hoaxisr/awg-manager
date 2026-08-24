package roles

import "testing"

// Дефолт потоков зависит от архитектуры: на mips полосу отнимает CPU (замеры
// KN-1010 в комментарии DefaultWorkers), на arm64 стена дальше. Границы
// проверяются на точных значениях арок, а не на «не ноль».
func TestDefaultWorkers(t *testing.T) {
	cases := []struct {
		goarch string
		want   int
	}{
		{"mips", 9},
		{"mipsle", 9},
		{"mips64", 9},
		{"mips64le", 9},
		{"arm64", 27},
		{"amd64", 27},
	}
	for _, c := range cases {
		got := DefaultWorkers(c.goarch)
		if got != c.want {
			t.Errorf("DefaultWorkers(%q) = %d, want %d", c.goarch, got, c.want)
		}
		// Клиент округляет -n вниз до кратного девяти и поднимает до девяти
		// минимум: некратный дефолт молча урезался бы в процессе.
		if got < 9 || got%9 != 0 {
			t.Errorf("DefaultWorkers(%q) = %d: не кратно девяти либо меньше девяти", c.goarch, got)
		}
	}
}

func TestRawExitDeclaredOnlyByRawClient(t *testing.T) {
	cases := []struct {
		name string
		cfg  RawExiter
		want RawExit
		ok   bool
	}{
		{"raw-клиент", WdttClientConfig{Mode: "raw", Name: "Германия",
			Peer: "1.2.3.4:56000", NdmsIface: "OpkgTun18", RawIface: "opkgtun18"},
			RawExit{NDMSName: "OpkgTun18", KernelIface: "opkgtun18",
				Name: "Германия", Peer: "1.2.3.4:56000"}, true},
		{"wg-клиент", WdttClientConfig{Mode: "wg", Name: "Голландия",
			Peer: "1.1.1.1:1", NdmsIface: "OpkgTun19"}, RawExit{}, false},
		{"сервер", WdttServerConfig{NdmsIface: "OpkgTun20", RawNdmsIface: "OpkgTun21"}, RawExit{}, false},
		{"freeturn-клиент", FreeTurnClientConfig{}, RawExit{}, false},
		{"freeturn-сервер", FreeTurnServerConfig{}, RawExit{}, false},
	}
	for _, c := range cases {
		got, ok := c.cfg.RawExit()
		if ok != c.ok {
			t.Fatalf("%s: ok = %v, want %v", c.name, ok, c.ok)
		}
		if got != c.want {
			t.Fatalf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestRawExitTrimsPeer(t *testing.T) {
	// Паритет со старым билдером: BuildRawTunnelRecord клал в запись
	// strings.TrimSpace(cfg.Peer) (raw_tunnel_meta.go:91). Пробел в
	// Peer.Endpoint ломает карточку и сравнение на идемпотентность.
	got, ok := WdttClientConfig{Mode: "raw", NdmsIface: "OpkgTun18",
		RawIface: "opkgtun18", Peer: "  1.2.3.4:56000  "}.RawExit()
	if !ok || got.Peer != "1.2.3.4:56000" {
		t.Fatalf("peer = %q", got.Peer)
	}
}
