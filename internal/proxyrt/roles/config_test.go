package roles

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
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
			Peer: "p", VKHashes: "h", Workers: 9, Obfs: "o",
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
			Bond: true, ObfProfile: "none", ObfKey: "k", ObfTimingMs: 20,
			StreamsPerCred: 1, Platform: "desktop", DNSMode: "auto",
			DNSServers: "s", ClientID: "c", Sub: "https://s", Debug: true,
			KCP: &FreeTurnKCP{Interval: 20}},
			[]string{"listen", "peer", "provider", "links", "streams", "transport",
				"mode", "bond", "obfProfile", "obfKey", "obfTimingMs",
				"streamsPerCred", "platform", "dnsMode", "dnsServers", "clientId",
				"sub", "debug", "kcp"}, nil},
		{"wdtt-server", WdttServerConfig{Listen: "l", WgPort: 1, ConfigDir: "c",
			WgIface: "wi", RawIface: "ri", NdmsIface: "n", RawNdmsIface: "rn",
			RawListen: "rl", DirectListen: "dl", RelayMode: "wg", NatMode: "none",
			NatStaticWAN: "w", Policy: "p", LanSegments: []string{"br0"},
			ExposeToPolicies: true, OpenFirewall: true, Debug: true},
			[]string{"listen", "wgPort", "configDir",
				"wgIface", "rawIface", "ndmsIface",
				"rawNdmsIface", "rawListen", "directListen", "relayMode",
				"natMode", "natStaticWan", "policy", "lanSegments",
				"exposeToPolicies", "openFirewall", "debug"}, nil},
		{"freeturn-server", FreeTurnServerConfig{Listen: "l", Connect: "c",
			LinkPeer: "vpn.example.org", Mode: "udp", ObfProfile: "none", ObfKey: "k",
			ClientsFile: "f", Debug: true, OpenFirewall: true},
			[]string{"listen", "connect", "linkPeer", "mode", "obfProfile", "obfKey",
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

// F144 (ревью): профиль KCP проверяется зеркально validateKCP клиента freeturn —
// частичный объект из чужой ссылки даёт нули, и клиент падал бы на старте.
func TestFreeTurnClientValidateKCP(t *testing.T) {
	base := FreeTurnClientConfig{Listen: "127.0.0.1:9001", Peer: "p", Mode: "tcp"}
	if err := base.Validate(); err != nil {
		t.Fatalf("без профиля: %v", err)
	}
	good := FreeTurnKCP{NoDelay: 1, Interval: 20, Resend: 2, NC: 1, SndWnd: 512, RcvWnd: 512, MTU: 1200, ACKNoDelay: true}
	base.KCP = &good
	if err := base.Validate(); err != nil {
		t.Fatalf("дефолт upstream: %v", err)
	}
	bad := map[string]FreeTurnKCP{
		"нули":      {},
		"nodelay=2": {NoDelay: 2, Interval: 20, Resend: 2, NC: 1, SndWnd: 512, RcvWnd: 512, MTU: 1200},
		"nc=2":      {NoDelay: 1, Interval: 20, Resend: 2, NC: 2, SndWnd: 512, RcvWnd: 512, MTU: 1200},
		"resend<0":  {NoDelay: 1, Interval: 20, Resend: -1, NC: 1, SndWnd: 512, RcvWnd: 512, MTU: 1200},
		"rcvwnd=0":  {NoDelay: 1, Interval: 20, Resend: 2, NC: 1, SndWnd: 512, MTU: 1200},
		"mtu=1400":  {NoDelay: 1, Interval: 20, Resend: 2, NC: 1, SndWnd: 512, RcvWnd: 512, MTU: 1400},
	}
	for name, k := range bad {
		k := k
		base.KCP = &k
		if err := base.Validate(); err == nil {
			t.Errorf("%s: ожидали отказ", name)
		}
	}
}

// Лимит bond — зеркало validateBond клиента 4.0: не больше 256/ссылок потоков,
// иначе процесс не стартует. Границы проверяются с обеих сторон.
func TestFreeTurnClientValidateBondLimit(t *testing.T) {
	base := FreeTurnClientConfig{Listen: "127.0.0.1:9000", Peer: "p:1", Provider: "vk",
		Links: "a, b ,c", Mode: "tcp", Bond: true}
	for _, tc := range []struct {
		name    string
		mut     func(*FreeTurnClientConfig)
		wantErr bool
	}{
		{"3 ссылки, 85 — граница", func(c *FreeTurnClientConfig) { c.Streams = 85 }, false},
		{"3 ссылки, 86", func(c *FreeTurnClientConfig) { c.Streams = 86 }, true},
		{"udp: -bond не уходит", func(c *FreeTurnClientConfig) { c.Streams, c.Mode = 86, "udp" }, false},
		{"bond выкл.", func(c *FreeTurnClientConfig) { c.Streams, c.Bond = 86, false }, false},
		{"direct: одна группа", func(c *FreeTurnClientConfig) { c.Streams, c.Provider = 256, "direct" }, false},
		{"без ссылок: одна группа", func(c *FreeTurnClientConfig) { c.Streams, c.Links = 257, "" }, true},
		{"0 = дефолт 12, 21 ссылка — граница", func(c *FreeTurnClientConfig) { c.Links = strings.Repeat("x,", 20) + "x" }, false},
		{"0 = дефолт 12, 22 ссылки", func(c *FreeTurnClientConfig) { c.Links = strings.Repeat("x,", 21) + "x" }, true},
	} {
		c := base
		tc.mut(&c)
		if err := c.Validate(); (err != nil) != tc.wantErr {
			t.Errorf("%s: err=%v, ждали ошибку=%t", tc.name, err, tc.wantErr)
		}
	}
}

func TestAutoReconnectValidation(t *testing.T) {
	ftBase := FreeTurnClientConfig{Listen: "127.0.0.1:9000", Peer: "p:1"}
	wdttBase := WdttClientConfig{Mode: "wg", Listen: "127.0.0.1:9000", Peer: "p:1", Password: "pwd", VKHashes: "vk"}

	validIntervals := []string{"", "on_failure", "30m", "1h", "2h", "4h", "8h", "12h", "24h"}
	for _, interval := range validIntervals {
		ft := ftBase
		ft.AutoReconnect = true
		ft.AutoReconnectInterval = interval
		if err := ft.Validate(); err != nil {
			t.Errorf("FreeTurnClientConfig.Validate() rejected valid interval %q: %v", interval, err)
		}

		wdtt := wdttBase
		wdtt.AutoReconnect = true
		wdtt.AutoReconnectInterval = interval
		if err := wdtt.Validate(); err != nil {
			t.Errorf("WdttClientConfig.Validate() rejected valid interval %q: %v", interval, err)
		}
	}

	invalidIntervals := []string{"1s", "5m", "garbage", "-1h", "100h"}
	for _, interval := range invalidIntervals {
		ft := ftBase
		ft.AutoReconnect = true
		ft.AutoReconnectInterval = interval
		if err := ft.Validate(); err == nil {
			t.Errorf("FreeTurnClientConfig.Validate() accepted invalid interval %q", interval)
		}

		wdtt := wdttBase
		wdtt.AutoReconnect = true
		wdtt.AutoReconnectInterval = interval
		if err := wdtt.Validate(); err == nil {
			t.Errorf("WdttClientConfig.Validate() accepted invalid interval %q", interval)
		}
	}

	// AutoReconnect = false: любой интервал допустим (игнорируется)
	ftDisabled := ftBase
	ftDisabled.AutoReconnect = false
	ftDisabled.AutoReconnectInterval = "garbage"
	if err := ftDisabled.Validate(); err != nil {
		t.Errorf("FreeTurnClientConfig.Validate() failed with AutoReconnect=false: %v", err)
	}
}

func TestParseReconnectInterval(t *testing.T) {
	if got := ParseReconnectInterval("30m"); got != 30*time.Minute {
		t.Errorf("30m: got %v, want %v", got, 30*time.Minute)
	}
	if got := ParseReconnectInterval("2h"); got != 2*time.Hour {
		t.Errorf("2h: got %v, want %v", got, 2*time.Hour)
	}
	if got := ParseReconnectInterval("on_failure"); got != 0 {
		t.Errorf("on_failure: got %v, want 0", got)
	}
	if got := ParseReconnectInterval(""); got != 0 {
		t.Errorf("empty: got %v, want 0", got)
	}
	if got := ParseReconnectInterval("garbage"); got != 0 {
		t.Errorf("garbage: got %v, want 0", got)
	}
}
