package roles

import (
	"strconv"

	"github.com/hoaxisr/awg-manager/awgmproto"
)

// Строители argv — чистые функции над валидным конфигом. Формы — паритет со
// старыми builder'ами (service.go:922, server.go:487, freeturn/service.go:876,
// 929). Зачем паритет: стабильность отпечатка МЕЖДУ РЕСТАРТАМИ НОВОГО
// менеджера (процессы старого поколения не усыновляются вовсе — их добивают,
// §9 протокола). Отсюда требование к писателю конфига (план 5): дефолты
// (obfs/fingerprint/captcha-mode/vk-auth/workers/listen) нормализуются ровно
// как в старых билдерах — иначе смена дефолта в UI меняет отпечаток и
// перезапускает живой процесс на пустом месте (M10 ревью).

func WdttClientArgs(c WdttClientConfig) []string {
	var args []string
	str := func(flag, val string) {
		if val != "" {
			args = append(args, flag, val)
		}
	}
	str("-listen", c.Listen)
	str("-peer", c.Peer)
	str("-password", c.Password)
	str("-vk", c.VKHashes)
	if c.Workers > 0 {
		args = append(args, "-n", strconv.Itoa(c.Workers))
	}
	str("-obfs", c.Obfs)
	str("-fingerprint", c.Fingerprint)
	str("-device-id", c.DeviceID)
	str("-captcha-mode", c.CaptchaMode)
	mode := c.VKAuthMode
	if mode == "" {
		mode = "vkcalls"
	}
	args = append(args, "-vk-auth-mode", mode)
	if c.Mode == "raw" {
		// -tun-fd-sock умер: дескриптор передаёт attach-tun протокола (§8).
		args = append(args, "-mode", "rawtun")
		str("-tun-name", c.RawIface)
	}
	return args
}

// Адрес шлюза WG-половины — константа старого мира
// (internal/wdtt/types.go:174-195).
const wdttServerGatewayAddr = "10.66.0.1"

func WdttServerArgs(c WdttServerConfig) []string {
	var args []string
	str := func(flag, val string) {
		if val != "" {
			args = append(args, flag, val)
		}
	}
	str("-listen", c.Listen)
	if c.WgPort > 0 {
		args = append(args, "-wg-port", strconv.Itoa(c.WgPort))
	}
	str("-config-dir", c.ConfigDir)
	str("-password", c.Password)
	args = append(args, "-no-nat") // NAT наш, безусловно (server.go:502)
	str("-wg-iface", c.WgIface)
	str("-raw-iface", c.RawIface)
	str("-listen-raw", c.EffectiveRawListen())
	if d := c.DirectListen; d != "" && d != c.Listen {
		str("-listen-direct", d)
	}
	// -dns: резолвер, который сервер объявляет абонентам; дефолт монолита
	// 8.8.8.8 уводит DNS мимо роутера (PR #697, F1).
	//
	// Флаг у форка ОДИН на обе половины: одно и то же значение уезжает и в
	// `[Interface] DNS` конфига WG-абонента (buildClientConfig), и в
	// `RAWCONF:ip|dns|mtu` raw-абонента (server.go форка) — выбрать разные
	// нечем. Режим связи здесь больше не спрашивается: половины работают
	// ОБЕ и всегда, а DNAT :53 стоит на обоих интерфейсах (wdttserver.Role
	// natGroups) и переписывает запрос на шлюз ТОЙ половины, по которой
	// абонент пришёл, независимо от объявленного адреса. Значение поэтому —
	// адрес роутера, годный обеим: 10.66.0.1 и 10.70.0.1 — это он сам.
	//
	// Прежде здесь стоял `10.70.66.1` — адрес, который форк присваивает
	// raw-TUN, когда поднимает его САМ. Под менеджером дескриптор приходит
	// извне (`awgmTakeTun`, server.go:2182), свой `ip addr add` форк
	// пропускает, и адреса этого на роутере не существует: абонент получал
	// резолвер в никуда.
	str("-dns", wdttServerGatewayAddr)
	return args
}

func FreeTurnClientArgs(c FreeTurnClientConfig) []string {
	var args []string
	str := func(flag, val string) {
		if val != "" {
			args = append(args, flag, val)
		}
	}
	flag := func(name string, on bool) {
		if on {
			args = append(args, name)
		}
	}
	str("-listen", c.Listen)
	str("-peer", c.Peer)
	str("-provider", c.Provider)
	str("-links", c.Links)
	if c.Streams > 0 {
		args = append(args, "-n", strconv.Itoa(c.Streams))
	}
	str("-transport", c.Transport)
	str("-mode", c.Mode)
	flag("-bond", c.Bond)
	str("-obf-profile", c.ObfProfile)
	str("-obf-key", c.ObfKey)
	if c.StreamsPerCred > 0 {
		args = append(args, "-streams-per-cred", strconv.Itoa(c.StreamsPerCred))
	}
	if c.Platform == "mobile" {
		str("-platform", c.Platform)
	}
	str("-dns-mode", c.DNSMode)
	str("-dns-servers", c.DNSServers)
	str("-client-id", c.ClientID)
	str("-sub", c.Sub)
	flag("-debug", c.Debug)
	return args
}

func FreeTurnServerArgs(c FreeTurnServerConfig) []string {
	var args []string
	str := func(flag, val string) {
		if val != "" {
			args = append(args, flag, val)
		}
	}
	str("-listen", c.Listen)
	str("-connect", c.Connect)
	str("-mode", c.Mode)
	str("-obf-profile", c.ObfProfile)
	str("-obf-key", c.ObfKey)
	str("-clients-file", c.ClientsFile)
	if c.Debug {
		args = append(args, "-debug")
	}
	return args
}

// WantHash — отпечаток конфигурации, с которой менеджер запустил БЫ процесс
// сейчас. Та же каноническая форма, что у процесса (§5.5): awgm-флаги
// выпадают внутри ConfigHash, поэтому их наличие в args безразлично.
func WantHash(args []string) string {
	return awgmproto.ConfigHash(args)
}
