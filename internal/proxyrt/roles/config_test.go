package roles

import (
	"encoding/json"
	"testing"
)

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

func TestStoreWireFormatCanary(t *testing.T) {
	// Формат proxy-instances.json менять только с миграцией. Ловит и
	// переименование поля Go без тега, и потерю тега.
	cases := []struct {
		name   string
		v      any
		want   []string // обязательные json-ключи при заполненных полях
		forbid []string // ключи, которых в формате быть не должно
	}{
		{"wdtt-client", WdttClientConfig{Mode: "raw", Name: "n", Listen: "l",
			Peer: "p", Password: "pw", VKHashes: "h", Workers: 9, Obfs: "o",
			Fingerprint: "f", DeviceID: "d", CaptchaMode: "auto", VKAuthMode: "v",
			NdmsIface: "OpkgTun18", RawIface: "opkgtun18",
			Policies: []PolicyPermit{{Name: "P", Order: 1}}},
			[]string{"connMode", "listen", "peer", "password", "vkHashes",
				"workers", "obfs", "fingerprint", "deviceId", "captchaMode",
				"vkAuthMode", "ndmsIface", "rawIface", "policies"},
			// Имя пишет только Record.Name (Р3): у поля конфига json:"-",
			// и запрет проверяется в ОБЕИХ формах ключа — без тега (Name)
			// и с любым тегом, который писатель мог бы завести (name).
			[]string{"Name", "name"}},
		{"policy-permit", PolicyPermit{Name: "P", Order: 1},
			[]string{"name", "order"}, nil},
		{"freeturn-client", FreeTurnClientConfig{Listen: "l", Peer: "p",
			Provider: "vk", Links: "x", Streams: 1, Transport: "tcp", Mode: "udp",
			Bond: true, TurnHost: "t", TurnPort: 1, ObfProfile: "none", ObfKey: "k",
			StreamsPerCred: 1, Platform: "desktop", DNSMode: "auto",
			DNSServers: "s", ClientID: "c", Sub: "https://s", Debug: true},
			[]string{"listen", "peer", "provider", "links", "streams", "transport",
				"mode", "bond", "turnHost", "turnPort", "obfProfile", "obfKey",
				"streamsPerCred", "platform", "dnsMode", "dnsServers", "clientId",
				"sub", "debug"}, nil},
		{"wdtt-server", WdttServerConfig{Listen: "l", WgPort: 1, ConfigDir: "c",
			Password: "pw", AdminID: "a", BotToken: "b", NatIface: "ni",
			WgIface: "wi", RawIface: "ri", NdmsIface: "n", RawNdmsIface: "rn",
			RawListen: "rl", DirectListen: "dl", RelayMode: "wg", NatMode: "none",
			NatStaticWAN: "w", Policy: "p", LanSegments: []string{"br0"},
			ExposeToPolicies: true, OpenFirewall: true, Debug: true},
			[]string{"listen", "wgPort", "configDir", "password", "adminId",
				"botToken", "natIface", "wgIface", "rawIface", "ndmsIface",
				"rawNdmsIface", "rawListen", "directListen", "relayMode",
				"natMode", "natStaticWan", "policy", "lanSegments",
				"exposeToPolicies", "openFirewall", "debug"}, nil},
		{"freeturn-server", FreeTurnServerConfig{Listen: "l", Connect: "c",
			Mode: "udp", ObfProfile: "none", ObfKey: "k", ClientsFile: "f",
			Debug: true, OpenFirewall: true},
			[]string{"listen", "connect", "mode", "obfProfile", "obfKey",
				"clientsFile", "debug", "openFirewall"}, nil},
	}
	for _, c := range cases {
		data, err := json.Marshal(c.v)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatal(err)
		}
		for _, k := range c.want {
			if _, ok := m[k]; !ok {
				t.Fatalf("%s: ключ %q пропал — формат store менять только с миграцией", c.name, k)
			}
		}
		for _, k := range c.forbid {
			if _, ok := m[k]; ok {
				t.Fatalf("%s: ключ %q сериализуется, а не должен — писатель имени один, Record.Name (Р3)", c.name, k)
			}
		}
	}
}
