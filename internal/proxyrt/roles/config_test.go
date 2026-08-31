package roles

import (
	"encoding/json"
	"strings"
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
			Policies: []PolicyPermit{{Name: "P", Order: orderPtr(0)}}},
			[]string{"connMode", "listen", "peer", "password", "vkHashes",
				"workers", "obfs", "fingerprint", "deviceId", "captchaMode",
				"vkAuthMode", "ndmsIface", "rawIface", "policies"},
			// Имя пишет только Record.Name (Р3): у поля конфига json:"-",
			// и запрет проверяется в ОБЕИХ формах ключа — без тега (Name)
			// и с любым тегом, который писатель мог бы завести (name).
			[]string{"Name", "name"}},
		// Order 0 — ЗАКОННАЯ верхняя позиция политики (нумерация NDMS с нуля,
		// ndms/query/policies.go:86), а не «не задано»: ключ обязан доехать.
		{"policy-permit-верх", PolicyPermit{Name: "P", Order: orderPtr(0)},
			[]string{"name", "order"}, nil},
		// Позиция не закреплена — ключа нет вовсе (в хвост, appendOrder).
		{"policy-permit-без-позиции", PolicyPermit{Name: "P"},
			[]string{"name"}, []string{"order"}},
		{"freeturn-client", FreeTurnClientConfig{Listen: "l", Peer: "p",
			Provider: "vk", Links: "x", Streams: 1, Transport: "tcp", Mode: "udp",
			Bond: true, ObfProfile: "none", ObfKey: "k",
			StreamsPerCred: 1, Platform: "desktop", DNSMode: "auto",
			DNSServers: "s", ClientID: "c", Sub: "https://s", Debug: true},
			[]string{"listen", "peer", "provider", "links", "streams", "transport",
				"mode", "bond", "obfProfile", "obfKey",
				"streamsPerCred", "platform", "dnsMode", "dnsServers", "clientId",
				"sub", "debug"}, nil},
		{"wdtt-server", WdttServerConfig{Listen: "l", WgPort: 1, ConfigDir: "c",
			Password: "pw",
			WgIface:  "wi", RawIface: "ri", NdmsIface: "n", RawNdmsIface: "rn",
			RawListen: "rl", DirectListen: "dl", RelayMode: "wg", NatMode: "none",
			NatStaticWAN: "w", Policy: "p", LanSegments: []string{"br0"},
			ExposeToPolicies: true, OpenFirewall: true, Debug: true},
			[]string{"listen", "wgPort", "configDir", "password",
				"wgIface", "rawIface", "ndmsIface",
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
		// Состав ключей закрыт с ОБЕИХ сторон: новый ключ в формате — тоже
		// миграция, и читатель старого файла обязан быть на него рассчитан.
		if len(m) != len(c.want) {
			t.Fatalf("%s: ключей %d (%v), ждали %d (%v) — состав формата менять только с миграцией",
				c.name, len(m), m, len(c.want), c.want)
		}
	}
}

func orderPtr(v int) *int { return &v }

// Сервер поднимает четыре сокета: DTLS, raw (по умолчанию DTLS+1), direct и
// userspace-WireGuard. Столкновение номеров валит один из них молча — в
// журнале форка, — поэтому конфиг обязан отказывать ДО старта.
func TestWdttServerValidateRejectsPortCollisions(t *testing.T) {
	base := func() WdttServerConfig {
		return WdttServerConfig{
			Listen: "0.0.0.0:56002", WgPort: 56001, RelayMode: "wg", NatMode: "none",
			NdmsIface: "OpkgTun17", WgIface: "opkgtun17",
			RawNdmsIface: "OpkgTun18", RawIface: "opkgtun18",
		}
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("дефолтная раскладка портов обязана проходить: %v", err)
	}

	cases := []struct {
		name string
		mut  func(*WdttServerConfig)
		want string
	}{
		// Ровно тот случай, ради которого проверка заведена: порт раздачи
		// 56000 отдаёт raw-половине 56001, а там уже стоит дефолтный WG.
		{"raw по умолчанию налетает на WG", func(c *WdttServerConfig) {
			c.Listen = "0.0.0.0:56000"
		}, "56001"},
		{"явный raw равен порту раздачи", func(c *WdttServerConfig) {
			c.RawListen = "0.0.0.0:56002"
		}, "56002"},
		{"WG равен порту раздачи", func(c *WdttServerConfig) {
			c.WgPort = 56002
		}, "56002"},
		{"direct равен raw", func(c *WdttServerConfig) {
			c.DirectListen = "0.0.0.0:56003"
		}, "56003"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := base()
			tc.mut(&c)
			err := c.Validate()
			if err == nil {
				t.Fatal("столкновение портов обязано быть отказом конфига")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("в отказе нет спорного порта %s: %v", tc.want, err)
			}
		})
	}
}

// DirectListen, равный Listen, — это «выключено» (та же трактовка в argv,
// INPUT-портах и ведомости занятости), а не столкновение.
func TestWdttServerValidateDirectEqualToListenIsOff(t *testing.T) {
	c := WdttServerConfig{
		Listen: "0.0.0.0:56002", DirectListen: "0.0.0.0:56002", WgPort: 56001,
		RelayMode: "wg", NatMode: "none",
		NdmsIface: "OpkgTun17", WgIface: "opkgtun17",
		RawNdmsIface: "OpkgTun18", RawIface: "opkgtun18",
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("direct == listen означает «выключено»: %v", err)
	}
}
