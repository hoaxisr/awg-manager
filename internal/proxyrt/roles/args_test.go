package roles

import (
	"strings"
	"testing"
)

func rawClient() WdttClientConfig {
	return WdttClientConfig{
		Mode: "raw", Listen: "127.0.0.1:9000", Peer: "vps.example:56003",
		Password: "pw", VKHashes: "h1,h2", Workers: 12,
		Obfs: "audio", Fingerprint: "chrome", DeviceID: "awgm-default",
		CaptchaMode: "rjs", VKAuthMode: "vkcalls",
		NdmsIface: "OpkgTun18", RawIface: "opkgtun18",
		Policies: []PolicyPermit{{Name: "Policy0"}},
	}
}

func TestWdttClientArgsRaw(t *testing.T) {
	got := strings.Join(WdttClientArgs(rawClient()), " ")
	// Форма — паритет с internal/wdtt/service.go:922 (buildClientArgs),
	// МИНУС -tun-fd-sock: передача дескриптора ушла в attach-tun протокола (§8).
	for _, want := range []string{
		"-listen 127.0.0.1:9000", "-peer vps.example:56003", "-password pw",
		"-vk h1,h2", "-n 12", "-obfs audio", "-fingerprint chrome",
		"-device-id awgm-default", "-captcha-mode rjs", "-vk-auth-mode vkcalls",
		"-mode rawtun", "-tun-name opkgtun18",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("нет %q в %q", want, got)
		}
	}
	if strings.Contains(got, "-tun-fd-sock") {
		t.Fatal("-tun-fd-sock умер вместе со старым сокетом передачи fd (§8 протокола)")
	}
}

func TestWdttClientArgsWgOmitsRawFlags(t *testing.T) {
	c := rawClient()
	c.Mode = "wg"
	got := strings.Join(WdttClientArgs(c), " ")
	if strings.Contains(got, "rawtun") || strings.Contains(got, "-tun-name") {
		t.Fatalf("wg-режим не должен нести raw-флаги: %q", got)
	}
}

func TestWdttServerArgs(t *testing.T) {
	c := WdttServerConfig{
		Listen: "0.0.0.0:56000", WgPort: 51820, ConfigDir: "/opt/etc/wdtt",
		WgIface: "opkgtun17", RawIface: "opkgtun19",
		NdmsIface: "OpkgTun17", RawNdmsIface: "OpkgTun19",
		RelayMode: "wg", NatMode: "full", Policy: "none",
	}
	got := strings.Join(WdttServerArgs(c), " ")
	// Паритет с internal/wdtt/server.go:487 (buildServerArgs): -no-nat
	// безусловен (NAT наш), -dns = шлюз, который видят клиенты (PR #697, F1).
	for _, want := range []string{
		"-listen 0.0.0.0:56000", "-wg-port 51820", "-config-dir /opt/etc/wdtt",
		"-no-nat", "-wg-iface opkgtun17", "-raw-iface opkgtun19",
		"-listen-raw 0.0.0.0:56001", "-dns 10.66.0.1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("нет %q в %q", want, got)
		}
	}
}

// Флаг -dns у форка один на обе половины, и режим связи его не выбирает:
// абонент любой половины получает адрес роутера, а DNAT на его интерфейсе
// переписывает запрос на шлюз своей половины. Прежде raw-режим подставлял
// 10.70.66.1 — адрес, которого под менеджером на роутере не существует.
func TestWdttServerArgsDNSIsRouterRegardlessOfRelayMode(t *testing.T) {
	for _, mode := range []string{"wg", "raw"} {
		c := WdttServerConfig{Listen: "0.0.0.0:56000", WgPort: 51820, RelayMode: mode}
		got := strings.Join(WdttServerArgs(c), " ")
		if !strings.Contains(got, "-dns 10.66.0.1") {
			t.Fatalf("режим %q: dns обязан быть адресом роутера, got %q", mode, got)
		}
		if strings.Contains(got, "10.70.66.1") {
			t.Fatalf("режим %q: 10.70.66.1 под менеджером не существует, got %q", mode, got)
		}
	}
}

// Строка запуска сверяется ЦЕЛИКОМ, а не тремя флагами. Причина не в
// аккуратности: argv — это отпечаток конфигурации процесса
// (`Proc.SetDesired` → `awgmproto.ConfigHash(forkArgs)`), по которому движок
// решает, надо ли перезапускать. Потерянный флаг означает сразу две беды:
// процесс работает без настройки, и его отпечаток молча совпадает с чужим.
// Прежняя редакция смотрела три подстроки из семнадцати аргументов —
// удаление `-transport` и `-mode` проходило зелёным.
//
// Ожидание — рукописная строка, а не рендер тем же построителем.
func TestFreeTurnArgs(t *testing.T) {
	cl := FreeTurnClientConfig{
		Listen: "127.0.0.1:9001", Peer: "relay.example:3478", Provider: "prov",
		Links: "l1,l2", Streams: 2, Transport: "udp", Mode: "turn", Bond: true,
		ObfProfile: "xor", ObfKey: "k", StreamsPerCred: 3, Platform: "mobile",
		DNSMode: "doh", DNSServers: "1.1.1.1", ClientID: "cid",
		Sub: "https://sub", Debug: true,
	}
	wantClient := "-listen 127.0.0.1:9001 -peer relay.example:3478 -provider prov " +
		"-links l1,l2 -n 2 -transport udp -mode turn -bond -obf-profile xor -obf-key k " +
		"-streams-per-cred 3 -platform mobile -dns-mode doh -dns-servers 1.1.1.1 " +
		"-client-id cid -sub https://sub -debug"
	if got := strings.Join(FreeTurnClientArgs(cl), " "); got != wantClient {
		t.Fatalf("argv клиента:\n%s\nждали:\n%s", got, wantClient)
	}

	// -platform уезжает ТОЛЬКО для mobile: у desktop его нет вовсе, и это не
	// косметика — лишний флаг сдвинул бы отпечаток всем настольным клиентам.
	cl.Platform = "desktop"
	if got := strings.Join(FreeTurnClientArgs(cl), " "); strings.Contains(got, "-platform") {
		t.Errorf("desktop не должен получать -platform: %q", got)
	}

	srv := FreeTurnServerConfig{
		Listen: "0.0.0.0:3478", Connect: "up:1", Mode: "udp",
		ObfProfile: "xor", ObfKey: "k", ClientsFile: "/opt/etc/ft/clients", Debug: true,
	}
	wantServer := "-listen 0.0.0.0:3478 -connect up:1 -mode udp -obf-profile xor " +
		"-obf-key k -clients-file /opt/etc/ft/clients -debug"
	if got := strings.Join(FreeTurnServerArgs(srv), " "); got != wantServer {
		t.Fatalf("argv сервера:\n%s\nждали:\n%s", got, wantServer)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name string
		err  string
		cfg  interface{ Validate() error }
	}{
		{"клиент без password", "password", func() WdttClientConfig { c := rawClient(); c.Password = ""; return c }()},
		{"клиент без peer", "peer", func() WdttClientConfig { c := rawClient(); c.Peer = ""; return c }()},
		{"клиент без vk", "vk", func() WdttClientConfig { c := rawClient(); c.VKHashes = ""; return c }()},
		{"raw-клиент без пина индекса", "индекс", func() WdttClientConfig { c := rawClient(); c.NdmsIface = ""; return c }()},
		{"клиент с нелокальным listen", "127.0.0.1", func() WdttClientConfig { c := rawClient(); c.Listen = "0.0.0.0:9000"; return c }()},
		{"кривой режим не чинится молча", "mode", func() WdttClientConfig { c := rawClient(); c.Mode = "RAW"; return c }()},
		{"internet-only без WAN", "natStaticWAN", WdttServerConfig{
			Listen: "0.0.0.0:56000", NatMode: "internet-only", RelayMode: "wg"}},
		{"сервер без WG-половины NDMS", "ndmsIface", WdttServerConfig{
			Listen: "0.0.0.0:56000", NatMode: "full", RelayMode: "wg",
			RawIface: "opkgtun18", RawNdmsIface: "OpkgTun18"}},
		{"сервер без raw-половины NDMS", "rawNdmsIface", WdttServerConfig{
			Listen: "0.0.0.0:56000", NatMode: "full", RelayMode: "wg",
			WgIface: "opkgtun17", NdmsIface: "OpkgTun17"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Fatalf("Validate = %v, ожидали упоминание %q", err, c.err)
			}
		})
	}
}

func TestWantHashStableAndIgnoresAwgmFlags(t *testing.T) {
	base := WdttClientArgs(rawClient())
	withAwgm := append(append([]string{}, base...),
		"--awgm-control-socket=/tmp/awgm/wt-client-client-default.sock",
		"--awgm-log-file=/tmp/awgm/wt-client-client-default.log")
	if WantHash(base) != WantHash(withAwgm) {
		t.Fatal("awgm-флаги обязаны выпадать из отпечатка (§5.5 п.1)")
	}
	other := rawClient()
	other.Password = "другой"
	if WantHash(base) == WantHash(WdttClientArgs(other)) {
		t.Fatal("смена пароля обязана менять отпечаток")
	}
}

// Порт из мусора не становится портом: Sscanf на "56000x" молча отдавал 56000,
// и raw-половина уезжала на 56001 вместо фолбэка ports.go.
func TestEffectiveRawListenFallsBackOnGarbagePort(t *testing.T) {
	c := WdttServerConfig{Listen: "0.0.0.0:56000x"}
	if got := c.EffectiveRawListen(); got != "0.0.0.0:56003" {
		t.Fatalf("EffectiveRawListen = %q, ожидали фолбэк 0.0.0.0:56003", got)
	}
}

// Пароль владельца необязателен: форк падает только когда паролей нет ВОВСЕ
// (`serverWrapKeys.Count() == 0`), а абонентские он берёт из passwords.json.
// Наше прежнее требование было строже форка и запирало сервер, у которого
// абоненты есть, а «главного пароля» никто не задавал — из UI это не чинилось.
func TestValidateServerWithoutOwnerPassword(t *testing.T) {
	cfg := WdttServerConfig{
		Listen: "0.0.0.0:56000", NatMode: "full", RelayMode: "wg",
		NdmsIface: "OpkgTun18", WgIface: "opkgtun18",
		RawNdmsIface: "OpkgTun19", RawIface: "opkgtun19",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("сервер обязан быть валиден: %v", err)
	}
}
