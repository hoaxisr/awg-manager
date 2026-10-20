package router

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/singbox/router/bypassset"
	"github.com/hoaxisr/awg-manager/internal/storage"
	sysexec "github.com/hoaxisr/awg-manager/internal/sys/exec"
	sysiptables "github.com/hoaxisr/awg-manager/internal/sys/iptables"
)

// Перехват IPv6 в режиме tproxy.
//
// Устроен как отдельный слой рядом с v4-механикой (iptables.go), а не как её
// параметризация: у v6 свои цепочки, свой хук netfilter.d, свои инбаунды
// sing-box и свой снимок применённого спека. Общим остаётся только то, что
// неделимо по природе: fwmark TPROXY, номер таблицы маршрутизации и процесс
// sing-box. Так v4-путь, обкатанный на железе, не меняется ни на байт при
// InterceptFamily == "ipv4_only".
//
// Отличия от v4, каждое проверено на роутере (KN-1811, ядро 4.9-ndm-5):
//
//   - UDP-инбаунд привязан к мосту (bind_interface). DNS клиенты шлют на
//     link-local адрес роутера (fe80::…, его раздаёт RA), а sing-box теряет
//     scope id исходного адреса назначения и отвечает с сокета, привязанного к
//     этому адресу: bind() на link-local без интерфейса ядро отвергает (EINVAL),
//     и ответ не уходит вовсе. С bind_interface сокет ответа привязан к мосту.
//     Отсюда инбаунд НА КАЖДЫЙ мост и правило TPROXY с `-i <мост>`.
//   - Приоритет `ip -6 rule` (1100) меньше 2160: с него у NDMS в IPv6 идут
//     `from all lookup <таблица WAN>`. В v4 такие правила тоже бывают
//     (site-to-site IPsec, 2000), поэтому v4 с F678 стоит на том же 1100.
//   - Селектор — метка ПАКЕТА плюс направление соединения. Метку по MAC NDMS
//     ставит на каждый пакет устройства; `--ctdir ORIGINAL` отсекает ответы
//     устройства на соединения, открытые снаружи: в IPv6 у устройств публичные
//     адреса, и без этого фильтра ответы на входящий UDP уходили бы в sing-box.
//   - Исключения по адресам динамические: публичный префикс LAN и адреса самого
//     роутера выдаёт провайдер, и они меняются.

// Значения storage.SingboxRouterSettings.InterceptFamily.
const (
	FamilyIPv4Only = "ipv4_only"
	FamilyBoth     = "both"
	FamilyIPv6Only = "ipv6_only"
)

// familyHasV4 / familyHasV6 — участвует ли семейство в перехвате. Пустая
// строка (поле не задано) читается как "ipv4_only".
func familyHasV4(family string) bool { return family != FamilyIPv6Only }
func familyHasV6(family string) bool { return family == FamilyBoth || family == FamilyIPv6Only }

const (
	// Redirect6Port — порт TCP-инбаунда redirect6-in. Слушает `::`: REDIRECT
	// переписывает назначение на адрес входного интерфейса, а не на loopback.
	Redirect6Port = 51273
	// TProxy6Port — порт общего UDP-инбаунда tproxy6-in без привязки к
	// интерфейсу. Обслуживает интерфейсы-источники (туннели): link-local DNS
	// там не бывает, а заводить порт на каждый туннель незачем.
	TProxy6Port = 51274
	// TProxy6PortBase — первый порт UDP-инбаундов tproxy6-in-<мост>; мост brN
	// получает base+N. Привязка к номеру моста, а не к позиции в списке: иначе
	// исчезновение одного моста сдвигало бы порты остальных и рвало их потоки.
	TProxy6PortBase = 51321
	// maxV6Bridges ограничивает диапазон портов (51321-51336).
	maxV6Bridges = 16

	Chain6Name     = "AWGM6-TPROXY"
	Redirect6Chain = "AWGM6-REDIRECT"
	// Blackhole6Chain — fail-closed DROP для IPv6, зеркало BlackholeChain:
	// ставится, только пока sing-box мёртв, а джампы перехвата снесены.
	Blackhole6Chain = "AWGM6-BLACKHOLE"

	// IPRule6Priority — приоритет `ip -6 rule` для fwmark TPROXY. Обязан быть
	// меньше 2160: с этого приоритета у NDMS идут `from all lookup <таблица>`.
	IPRule6Priority = 1100

	redirect6Tag     = "redirect6-in"
	tproxy6Tag0      = "tproxy6-in"
	tproxy6TagPrefix = "tproxy6-in-"
	tproxy6Listen    = "::1"
	redirect6Listen  = "::"
)

// Пути файлов v6-слоя. Перенацеливаются вместе с v4 (SetDataDir).
var (
	netfilter6RulesPath       = "/opt/etc/awg-manager/singbox/router-netfilter6.rules"
	netfilter6MangleRulesPath = "/opt/etc/awg-manager/singbox/router-netfilter6-mangle.rules"
	netfilter6NatRulesPath    = "/opt/etc/awg-manager/singbox/router-netfilter6-nat.rules"
	netfilter6BlackholePath   = "/opt/etc/awg-manager/singbox/router-blackhole6.rules"
	// bypassSave6Path — дамп `ipset save` набора AWGM-BYPASS6: по нему v6-хук
	// возвращает набор после перезагрузки роутера, до ip6tables-restore.
	bypassSave6Path = "/opt/etc/awg-manager/singbox/bypass6.ipset"
	// netfilterCtClean6Path — скрипт чистки потоков v6-слоя (ctClean6Script).
	netfilterCtClean6Path = "/opt/etc/awg-manager/singbox/awgm-ctclean6.sh"
)

// netfilter6HookName — имя v6-хука. Лежит рядом с v4-хуком: каталог берётся у
// него, поэтому песочница, уводящая v4-хук (SetNetfilterHookPath), уводит и
// этот.
const netfilter6HookName = "53-awgm-tproxy6.sh"

func netfilter6HookPath() string {
	return filepath.Join(filepath.Dir(netfilterHookPath), netfilter6HookName)
}

// bypassCIDRs6 — статические назначения, которые никогда не идут в sing-box:
// loopback, link-local, ULA и multicast. Перехват DNS стоит ВЫШЕ них, как и в
// v4: запрос на link-local адрес роутера обязан перехватываться.
var bypassCIDRs6 = []string{
	"::1/128",
	"fe80::/10",
	"fc00::/7",
	"ff00::/8",
}

// V6Bridge — LAN-мост и порт его UDP-инбаунда.
type V6Bridge struct {
	Name       string
	TProxyPort int
}

// V6Spec — входы v6-правил. Как и RestoreInputSpec, сравнивается по рендеру.
type V6Spec struct {
	// PolicyMark — метка политики NDMS (hex). Пусто при MatchAll.
	PolicyMark string
	// MatchAll — режим «все устройства»: селектор без метки, только по мосту.
	MatchAll bool
	// Bridges — LAN-мосты с IPv6, отсортированы по имени.
	Bridges []V6Bridge
	// LocalCIDRs — динамические исключения: глобальные адреса роутера (/128)
	// и on-link префиксы мостов. Отсортированы.
	LocalCIDRs []string

	BypassUDPPorts []PortRange
	BypassTCPPorts []PortRange

	// BypassCIDRs — IPv6-часть пользовательских исключений по подсетям.
	// Ранний RETURN в начале обеих цепочек, ДО перехвата DNS — та же
	// семантика полного обхода, что у RestoreInputSpec.BypassCIDRs.
	BypassCIDRs []string
	// BypassGeoIPSet — рядом с ними встаёт RETURN по набору AWGM-BYPASS6.
	// Набор обязан существовать до ip6tables-restore.
	BypassGeoIPSet bool
	// IngressInterfaces — kernel-имена интерфейсов-источников (туннели), чей
	// входящий трафик заворачивается наравне с мостами. В отличие от v4, где
	// их пакеты помечаются меткой политики, здесь у каждого свой джамп: метки
	// по MAC у туннеля нет, а селектор v6 — по интерфейсу.
	IngressInterfaces []string
	// QoSClasses — классы DSCP с портами IPv6-пары инбаундов.
	QoSClasses []QoSClassSpec
	// Uplinks — интерфейсы маршрутов по умолчанию; заполняется только в режиме
	// «все устройства» (см. emitUplinkReturns6).
	Uplinks []string
}

func equalV6Spec(a, b *V6Spec) bool {
	if a == nil || b == nil {
		return a == b
	}
	return buildRestoreInput6(*a) == buildRestoreInput6(*b)
}

// v6BridgePort возвращает порт UDP-инбаунда моста. ok=false для имени не вида
// brN и для N за пределами диапазона — такой мост в перехват не попадает.
func v6BridgePort(bridge string) (int, bool) {
	num, found := strings.CutPrefix(bridge, "br")
	if !found || num == "" {
		return 0, false
	}
	n, err := strconv.Atoi(num)
	if err != nil || n < 0 || n >= maxV6Bridges || strconv.Itoa(n) != num {
		return 0, false
	}
	return TProxy6PortBase + n, true
}

func tproxy6Tag(bridge string) string { return tproxy6TagPrefix + bridge }

// isV6InboundTag — принадлежит ли тег инбаундам v6-слоя.
func isV6InboundTag(tag string) bool {
	return tag == redirect6Tag || tag == tproxy6Tag0 || strings.HasPrefix(tag, tproxy6TagPrefix)
}

// v6ListenPorts — все порты, которые может занять v6-слой (для резерва),
// включая IPv6-пары классов QoS.
func v6ListenPorts() []int {
	ports := []int{Redirect6Port, TProxy6Port}
	for n := 0; n < maxV6Bridges; n++ {
		ports = append(ports, TProxy6PortBase+n)
	}
	for slot := 0; slot < MaxQoSClasses; slot++ {
		tp, rp := QoSClassPorts6(slot)
		ports = append(ports, tp, rp)
	}
	return ports
}

// v6Selector рендерит общую часть условия PREROUTING: мост и метка.
func v6Selector(bridge string, spec V6Spec) string {
	return fmt.Sprintf("-i %s -m mark --mark %s", bridge, spec.PolicyMark)
}

// v6BridgeJumps — есть ли у спека поинтерфейсные джампы с мостов. Это режим
// политики с известной меткой; без метки их нет (та же защитная ветка, что у
// emitPreroutingJump). В режиме «все устройства» джамп один и к интерфейсу не
// привязан — см. emitJumps6.
func v6BridgeJumps(spec V6Spec) bool {
	return !spec.MatchAll && len(spec.Bridges) > 0 && spec.PolicyMark != ""
}

// v6HasJumps — перехватывает ли спек хоть что-то.
func v6HasJumps(spec V6Spec) bool {
	return spec.MatchAll || v6BridgeJumps(spec) || len(spec.IngressInterfaces) > 0
}

// v6NeedsGeneric — нужен ли общий UDP-инбаунд без привязки к интерфейсу: в
// режиме «все устройства» в него идёт всё, что пришло не с моста, в режиме
// политики — интерфейсы-источники.
func v6NeedsGeneric(spec V6Spec) bool {
	return spec.MatchAll || len(spec.IngressInterfaces) > 0
}

// emitJumps6 рендерит PREROUTING-джампы в chain. Общие для перехвата и для
// fail-closed блокировки: селектор у них обязан совпадать по построению —
// блокировка дропает ровно то, что уехало бы в sing-box.
//
// Режим «все устройства» — один джамп без интерфейса, как в v4: в перехват
// попадает трафик с любого интерфейса, включая туннели. Аплинки отсекаются
// внутри цепочки (emitUplinkReturns6). directional добавляет фильтр
// направления соединения (нужен в mangle, см. шапку файла).
func emitJumps6(b *strings.Builder, chain string, spec V6Spec, directional bool) {
	dir := ""
	if directional {
		dir = " -m conntrack --ctdir ORIGINAL"
	}
	if spec.MatchAll {
		fmt.Fprintf(b, "-A PREROUTING%s -j %s\n", dir, chain)
		return
	}
	if v6BridgeJumps(spec) {
		for _, br := range spec.Bridges {
			fmt.Fprintf(b, "-A PREROUTING %s%s -j %s\n", v6Selector(br.Name, spec), dir, chain)
		}
	}
	for _, iface := range spec.IngressInterfaces {
		fmt.Fprintf(b, "-A PREROUTING -i %s%s -j %s\n", iface, dir, chain)
	}
}

// emitUplinkReturns6 — в режиме «все устройства» трафик, ВОШЕДШИЙ через
// аплинк, в sing-box не идёт. В v4 такой трафик отсекают исключения по
// адресам: за NAT он адресован либо самому роутеру, либо частной сети. В IPv6
// NAT нет, и входящие соединения из интернета идут к публичным адресам
// устройств; префикс LAN исключён, но сеть за LAN (делегированный префикс) —
// нет, поэтому отсекаем по интерфейсу.
//
// Первым отсекается lo. Локальный трафик роутера тоже проходит PREROUTING, и
// джамп без интерфейса завернул бы собственные DNS-запросы роутера (включая
// запросы самого sing-box к системному резолверу) обратно в sing-box —
// правило перехвата DNS стоит выше исключения ::1.
func emitUplinkReturns6(b *strings.Builder, chain string, spec V6Spec) {
	if !spec.MatchAll {
		return
	}
	fmt.Fprintf(b, "-A %s -i lo -j RETURN\n", chain)
	for _, iface := range spec.Uplinks {
		fmt.Fprintf(b, "-A %s -i %s -j RETURN\n", chain, iface)
	}
}

// emitUserBypass6 — ранние RETURN'ы полного обхода: пользовательские подсети
// и geoip-набор. В начале цепочки, до перехвата DNS, как и в v4.
func emitUserBypass6(b *strings.Builder, chain string, spec V6Spec) {
	for _, cidr := range spec.BypassCIDRs {
		fmt.Fprintf(b, "-A %s -d %s -j RETURN\n", chain, cidr)
	}
	if spec.BypassGeoIPSet {
		fmt.Fprintf(b, "-A %s -m set --match-set %s dst -j RETURN\n", chain, bypassset.SetName6)
	}
}

func emitBypassReturns6(b *strings.Builder, chain string, local []string) {
	for _, cidr := range bypassCIDRs6 {
		fmt.Fprintf(b, "-A %s -d %s -j RETURN\n", chain, cidr)
	}
	for _, cidr := range local {
		fmt.Fprintf(b, "-A %s -d %s -j RETURN\n", chain, cidr)
	}
}

func buildRestoreInput6(spec V6Spec) string {
	return buildMangleRestoreInput6(spec) + buildNatRestoreInput6(spec)
}

// emitTProxy6 рендерит одно правило TPROXY. match — условие перед целью
// («-i br0 -p udp --dport 53», «-p udp -m dscp --dscp 46» и т.п.).
func emitTProxy6(b *strings.Builder, match string, port int) {
	fmt.Fprintf(b, "-A %s %s -j TPROXY --on-port %d --on-ip %s --tproxy-mark 0x%x\n",
		Chain6Name, match, port, tproxy6Listen, Fwmark)
}

// emitGenericTProxy6 — правила в общий инбаунд. В режиме «все устройства» —
// одно правило без интерфейса (всё, что не забрали правила мостов выше), в
// режиме политики — по правилу на интерфейс-источник.
func emitGenericTProxy6(b *strings.Builder, proto string, spec V6Spec) {
	if spec.MatchAll {
		emitTProxy6(b, proto, TProxy6Port)
		return
	}
	for _, iface := range spec.IngressInterfaces {
		emitTProxy6(b, fmt.Sprintf("-i %s %s", iface, proto), TProxy6Port)
	}
}

// buildMangleRestoreInput6 — UDP через TPROXY. Порядок тот же, что в v4:
// полный обход (подсети, набор, порты), перехват DNS, исключения по адресам,
// диспатч классов QoS, catch-all.
//
// Правила TPROXY мостов несут `-i`: у каждого моста свой инбаунд (см. шапку
// файла); всё остальное идёт в общий. Диспатч по DSCP — без `-i`: инбаунд
// класса один на всех.
func buildMangleRestoreInput6(spec V6Spec) string {
	var b strings.Builder
	b.WriteString("*mangle\n")
	fmt.Fprintf(&b, ":%s - [0:0]\n", Chain6Name)
	emitUplinkReturns6(&b, Chain6Name, spec)
	emitUserBypass6(&b, Chain6Name, spec)
	for _, pr := range spec.BypassUDPPorts {
		fmt.Fprintf(&b, "-A %s -p udp --dport %s -j RETURN\n", Chain6Name, pr.String())
	}
	for _, br := range spec.Bridges {
		emitTProxy6(&b, fmt.Sprintf("-i %s -p udp --dport 53", br.Name), br.TProxyPort)
	}
	emitGenericTProxy6(&b, "-p udp --dport 53", spec)
	emitBypassReturns6(&b, Chain6Name, spec.LocalCIDRs)
	for _, q := range spec.QoSClasses {
		if q.Bypass {
			fmt.Fprintf(&b, "-A %s -p udp -m dscp --dscp %d -j RETURN\n", Chain6Name, q.DSCP)
			continue
		}
		emitTProxy6(&b, fmt.Sprintf("-p udp -m dscp --dscp %d", q.DSCP), q.TProxyPort)
	}
	for _, br := range spec.Bridges {
		emitTProxy6(&b, fmt.Sprintf("-i %s -p udp", br.Name), br.TProxyPort)
	}
	emitGenericTProxy6(&b, "-p udp", spec)
	emitJumps6(&b, Chain6Name, spec, true)
	emitSelfLoopDrop(&b)
	b.WriteString("COMMIT\n")
	return b.String()
}

// buildNatRestoreInput6 — TCP через REDIRECT. Фильтр направления не нужен:
// таблицу nat проходит только первый пакет соединения, а его шлёт инициатор.
func buildNatRestoreInput6(spec V6Spec) string {
	var b strings.Builder
	b.WriteString("*nat\n")
	fmt.Fprintf(&b, ":%s - [0:0]\n", Redirect6Chain)
	emitUplinkReturns6(&b, Redirect6Chain, spec)
	emitUserBypass6(&b, Redirect6Chain, spec)
	emitBypassReturns6(&b, Redirect6Chain, spec.LocalCIDRs)
	for _, pr := range spec.BypassTCPPorts {
		fmt.Fprintf(&b, "-A %s -p tcp --dport %s -j RETURN\n", Redirect6Chain, pr.String())
	}
	// Диспатч классов QoS. Перед ним — вырезка TCP/53 на основной порт, по той
	// же причине, что в v4: помеченный DSCP DNS-over-TCP иначе ушёл бы в
	// инбаунд класса мимо hijack-dns.
	if len(spec.QoSClasses) > 0 {
		fmt.Fprintf(&b, "-A %s -p tcp --dport 53 -j REDIRECT --to-ports %d\n", Redirect6Chain, Redirect6Port)
		for _, q := range spec.QoSClasses {
			if q.Bypass {
				fmt.Fprintf(&b, "-A %s -p tcp -m dscp --dscp %d -j RETURN\n", Redirect6Chain, q.DSCP)
				continue
			}
			fmt.Fprintf(&b, "-A %s -p tcp -m dscp --dscp %d -j REDIRECT --to-ports %d\n",
				Redirect6Chain, q.DSCP, q.RedirectPort)
		}
	}
	fmt.Fprintf(&b, "-A %s -p tcp -j REDIRECT --to-ports %d\n", Redirect6Chain, Redirect6Port)
	emitJumps6(&b, Redirect6Chain, spec, false)
	b.WriteString("COMMIT\n")
	return b.String()
}

// buildBlackholeRestoreInput6 — fail-closed блокировка: зеркало
// buildBlackholeRestoreInput. Те же исключения, что у перехвата (полный обход,
// порты обоих протоколов, адреса роутера и LAN), затем DROP; вход — тем же
// селектором (emitJumps6). Только mangle: там правило видит каждый пакет
// потока, а не только первый.
func buildBlackholeRestoreInput6(spec V6Spec) string {
	var b strings.Builder
	b.WriteString("*mangle\n")
	fmt.Fprintf(&b, ":%s - [0:0]\n", Blackhole6Chain)
	emitUplinkReturns6(&b, Blackhole6Chain, spec)
	emitUserBypass6(&b, Blackhole6Chain, spec)
	for _, pr := range spec.BypassUDPPorts {
		fmt.Fprintf(&b, "-A %s -p udp --dport %s -j RETURN\n", Blackhole6Chain, pr.String())
	}
	for _, pr := range spec.BypassTCPPorts {
		fmt.Fprintf(&b, "-A %s -p tcp --dport %s -j RETURN\n", Blackhole6Chain, pr.String())
	}
	emitBypassReturns6(&b, Blackhole6Chain, spec.LocalCIDRs)
	for _, q := range spec.QoSClasses {
		if q.Bypass {
			fmt.Fprintf(&b, "-A %s -m dscp --dscp %d -j RETURN\n", Blackhole6Chain, q.DSCP)
		}
	}
	fmt.Fprintf(&b, "-A %s -j DROP\n", Blackhole6Chain)
	emitJumps6(&b, Blackhole6Chain, spec, true)
	b.WriteString("COMMIT\n")
	return b.String()
}

// equalBlackholeSpec6 сравнивает спеки блокировки по её собственному рендеру —
// по тем же причинам, что equalBlackholeSpec.
func equalBlackholeSpec6(a, b *V6Spec) bool {
	if a == nil || b == nil {
		return a == b
	}
	return buildBlackholeRestoreInput6(*a) == buildBlackholeRestoreInput6(*b)
}

// ---- топология: мосты и локальные адреса --------------------------------

// v6Topology — то, что v6-правилам нужно знать о роутере.
type v6Topology struct {
	Bridges    []string
	LocalCIDRs []string
	// Uplinks — интерфейсы, через которые уходит маршрут по умолчанию IPv6
	// (в любой таблице). Нужны режиму «все устройства».
	Uplinks []string
}

// collectV6Topology — шов над чтением /proc/net/if_inet6, /proc/net/ipv6_route
// и /sys.
var collectV6Topology = readV6Topology

func readV6Topology() (v6Topology, error) {
	data, err := os.ReadFile("/proc/net/if_inet6")
	if err != nil {
		return v6Topology{}, err
	}
	topo := parseIfInet6(string(data), isLinuxBridge)
	// Отказ чтения маршрутов — не повод отменять перехват: без списка аплинков
	// режим «все устройства» остаётся с исключениями по адресам, как в v4.
	if routes, rerr := os.ReadFile("/proc/net/ipv6_route"); rerr == nil {
		topo.Uplinks = parseIPv6DefaultIfaces(string(routes))
	}
	return topo, nil
}

// parseIPv6DefaultIfaces достаёт из /proc/net/ipv6_route интерфейсы маршрутов
// по умолчанию. Колонки: назначение(32 hex) длина(2 hex) источник(32) длина(2)
// шлюз(32) метрика флаги счётчики… интерфейс. Файл перечисляет маршруты всех
// таблиц, поэтому сюда попадают и аплинки политик. lo не аплинк: через него
// идёт наш же маршрут `local ::/0` таблицы TPROXY.
func parseIPv6DefaultIfaces(data string) []string {
	const zeros = "00000000000000000000000000000000"
	seen := map[string]struct{}{}
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) < 10 || f[0] != zeros || f[1] != "00" {
			continue
		}
		if iface := f[len(f)-1]; iface != "lo" {
			seen[iface] = struct{}{}
		}
	}
	return sortedKeys(seen)
}

// Области адресов в /proc/net/if_inet6 (колонка scope, hex).
const (
	ifInet6ScopeGlobal = 0x00
	ifInet6ScopeLink   = 0x20
)

// parseIfInet6 разбирает /proc/net/if_inet6. Формат строки:
//
//	<адрес, 32 hex> <ifindex> <длина префикса> <scope> <флаги> <имя>
//
// Мост попадает в Bridges, если у него есть link-local адрес — то есть IPv6 на
// нём действительно поднят. В LocalCIDRs идут глобальные адреса роутера (/128)
// и префиксы мостов; ULA пропускаются — их целиком покрывает fc00::/7.
func parseIfInet6(data string, isBridge func(string) bool) v6Topology {
	bridges := map[string]struct{}{}
	local := map[string]struct{}{}
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) < 6 {
			continue
		}
		raw, err := hex.DecodeString(f[0])
		if err != nil || len(raw) != 16 {
			continue
		}
		plen, err1 := strconv.ParseInt(f[2], 16, 32)
		scope, err2 := strconv.ParseInt(f[3], 16, 32)
		if err1 != nil || err2 != nil {
			continue
		}
		dev := f[5]
		addr := netip.AddrFrom16([16]byte(raw))
		bridge := isBridge(dev)
		switch scope {
		case ifInet6ScopeLink:
			if bridge {
				bridges[dev] = struct{}{}
			}
		case ifInet6ScopeGlobal:
			if raw[0]&0xfe == 0xfc { // fc00::/7
				continue
			}
			local[netip.PrefixFrom(addr, 128).String()] = struct{}{}
			if bridge && plen > 0 && plen < 128 {
				local[netip.PrefixFrom(addr, int(plen)).Masked().String()] = struct{}{}
			}
		}
	}
	return v6Topology{Bridges: sortedKeys(bridges), LocalCIDRs: sortedKeys(local)}
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// v6BridgesFor превращает имена мостов в пары (мост, порт). Мосты без порта
// (имя не brN или N вне диапазона) возвращаются отдельно — вызывающий пишет
// о них в журнал.
func v6BridgesFor(names []string) (bridges []V6Bridge, skipped []string) {
	for _, name := range names {
		port, ok := v6BridgePort(name)
		if !ok {
			skipped = append(skipped, name)
			continue
		}
		bridges = append(bridges, V6Bridge{Name: name, TProxyPort: port})
	}
	return bridges, skipped
}

// buildV6Spec собирает желаемый спек v6-правил. Единственное место сборки —
// по той же причине, что у buildTproxySpec: enable и reconcile обязаны давать
// один и тот же спек на одних входах.
//
// ingress — уже резолвленные kernel-имена интерфейсов-источников; adhoc —
// рантайм-исключения по адресам (сейчас это IPv6-адреса KeenDNS с роутера).
// Классы QoS сюда не входят: их проекцию вызывающий дописывает в спек после
// проверки xt_dscp — той же, что гейтит классы в v4.
//
// Ошибка — нечитаемые исключения (порты, подсети). Спек при этом всё равно
// собран, без нечитаемой части: перехват важнее исключения, а что с ошибкой
// делать, решает вызывающий.
func buildV6Spec(sr storage.SingboxRouterSettings, mark string, policyMode bool, topo v6Topology, ingress, adhoc []string) (V6Spec, []string, error) {
	bridges, skipped := v6BridgesFor(topo.Bridges)
	bypassUDP, bypassTCP, portsErr := resolveBypassPorts(sr.BypassPresets, sr.BypassExtraPorts)
	if portsErr != nil {
		portsErr = fmt.Errorf("порты: %w", portsErr)
	}
	// Порядок и дедуп — как у resolveBypassCIDRs: сперва рантайм-исключения,
	// затем пользовательский список.
	var bypassCIDRs []string
	seen := map[string]bool{}
	user, subnetsErr := resolveBypassSubnets6(sr.BypassExtraSubnets)
	if subnetsErr != nil {
		subnetsErr = fmt.Errorf("подсети: %w", subnetsErr)
	}
	specErr := errors.Join(portsErr, subnetsErr)
	for _, c := range append(append([]string{}, adhoc...), user...) {
		if !seen[c] {
			seen[c] = true
			bypassCIDRs = append(bypassCIDRs, c)
		}
	}
	spec := V6Spec{
		PolicyMark:     mark,
		MatchAll:       !policyMode,
		Bridges:        bridges,
		LocalCIDRs:     topo.LocalCIDRs,
		BypassUDPPorts: bypassUDP,
		BypassTCPPorts: bypassTCP,
		BypassCIDRs:    bypassCIDRs,
		BypassGeoIPSet: len(sr.BypassGeoIPTags) > 0,
	}
	if spec.MatchAll {
		// «Все устройства»: джамп не привязан к интерфейсу, и отдельный список
		// источников не нужен — туннели попадают в перехват сами, как в v4.
		spec.Uplinks = topo.Uplinks
		return spec, skipped, specErr
	}
	// Источник, оказавшийся мостом, уже покрыт его собственными правилами.
	isBridge := make(map[string]bool, len(topo.Bridges))
	for _, name := range topo.Bridges {
		isBridge[name] = true
	}
	for _, name := range ingress {
		if !isBridge[name] {
			spec.IngressInterfaces = append(spec.IngressInterfaces, name)
		}
	}
	return spec, skipped, specErr
}

// ---- инбаунды sing-box ---------------------------------------------------

// ensureV6Inbounds сводит инбаунды v6-слоя к каноническому виду: сносит все
// свои и, если слой включён и спеку есть что перехватывать, добавляет
// redirect6-in, по tproxy6-in-<мост> на каждый мост и общий tproxy6-in при
// интерфейсах-источниках. Пересборка вместо правки на месте — как у
// ensureQoSInbounds: весь набор производный. Возвращает признак изменения.
//
// Инбаунды классов QoS сюда не входят — их IPv6-пары ведёт ensureQoSInbounds.
func ensureV6Inbounds(in []Inbound, spec V6Spec, enabled bool, udpTimeout string, udpNATMax int) ([]Inbound, bool) {
	effective := resolveUDPTimeout(udpTimeout)
	kept := make([]Inbound, 0, len(in)+len(spec.Bridges)+2)
	var had []Inbound
	for _, i := range in {
		if isV6InboundTag(i.Tag) {
			had = append(had, i)
			continue
		}
		kept = append(kept, i)
	}
	var want []Inbound
	if enabled && v6HasJumps(spec) {
		want = append(want, Inbound{
			Type:        "redirect",
			Tag:         redirect6Tag,
			Listen:      redirect6Listen,
			ListenPort:  Redirect6Port,
			TCPFastOpen: true,
		})
		udp := func(tag string, port int, bind string) Inbound {
			return Inbound{
				Type:          "tproxy",
				Tag:           tag,
				Listen:        tproxy6Listen,
				ListenPort:    port,
				Network:       "udp",
				BindInterface: bind,
				UDPFragment:   true,
				UDPTimeout:    effective,
				UDPNATMax:     udpNATMax,
			}
		}
		if v6BridgeInbounds(spec) {
			for _, br := range spec.Bridges {
				want = append(want, udp(tproxy6Tag(br.Name), br.TProxyPort, br.Name))
			}
		}
		if v6NeedsGeneric(spec) {
			want = append(want, udp(tproxy6Tag0, TProxy6Port, ""))
		}
	}
	return append(kept, want...), !reflect.DeepEqual(had, want)
}

// v6SocketsProbe — шов над проверкой сокетов v6-инбаундов.
var v6SocketsProbe = v6SocketsBound

// v6SocketsBound сообщает, слушает ли sing-box все инбаунды спека. Правила в
// непривязанный сокет ставить нельзя по той же причине, что и в v4 (#221).
func v6SocketsBound(spec V6Spec) bool {
	tcp, err := os.ReadFile("/proc/net/tcp6")
	if err != nil || !localPortInState(string(tcp), Redirect6Port, tcpStateListen) {
		return false
	}
	udp, err := os.ReadFile("/proc/net/udp6")
	if err != nil {
		return false
	}
	if v6BridgeInbounds(spec) {
		for _, br := range spec.Bridges {
			if !localPortInState(string(udp), br.TProxyPort, udpStateBound) {
				return false
			}
		}
	}
	if v6NeedsGeneric(spec) && !localPortInState(string(udp), TProxy6Port, udpStateBound) {
		return false
	}
	return true
}

// v6BridgeInbounds — нужны ли спеку инбаунды мостов: в режиме политики при
// известной метке, в режиме «все устройства» — всегда, когда мосты есть.
func v6BridgeInbounds(spec V6Spec) bool {
	return len(spec.Bridges) > 0 && (spec.MatchAll || spec.PolicyMark != "")
}

// ---- установка и снос ----------------------------------------------------

// v6Backend — всё, чем v6-слой трогает систему: ip6tables, файлы правил и
// хука, чистка потоков. Интерфейсом, а не набором полей-функций: реализацию
// нельзя собрать наполовину (тип без метода не компилируется), и у слоя нет
// состояния «зависимости подключены не все», которое пришлось бы проверять во
// время работы.
type v6Backend interface {
	// Restore — `ip6tables-restore --noflush`.
	Restore(ctx context.Context, input string) error
	// Run / RunOutput — одиночный вызов ip6tables.
	Run(ctx context.Context, args ...string) error
	RunOutput(ctx context.Context, args ...string) (string, error)
	// PersistRules пишет файлы правил перехвата (общий и по таблицам).
	PersistRules(combined, mangle, nat string) error
	// PersistHook пишет хук netfilter.d и скрипт чистки потоков.
	PersistHook() error
	// RemoveFiles удаляет файлы правил, хук и скрипт чистки.
	RemoveFiles()
	// CleanFlows запускает скрипт чистки потоков (ctClean6Script).
	CleanFlows(ctx context.Context)
	// PersistBlackhole / RemoveBlackholeFile — файл правил блокировки.
	PersistBlackhole(input string) error
	RemoveBlackholeFile()
}

// systemV6 — боевая реализация v6Backend.
type systemV6 struct{}

func (systemV6) Restore(ctx context.Context, input string) error {
	return sysiptables.RestoreNoflush6(ctx, input)
}

func (systemV6) Run(ctx context.Context, args ...string) error {
	return sysiptables.Run6(ctx, args...)
}

func (systemV6) RunOutput(ctx context.Context, args ...string) (string, error) {
	return sysiptables.RunOutput6(ctx, args...)
}

func (systemV6) PersistRules(combined, mangle, nat string) error {
	if err := storage.AtomicWritePerm(netfilter6RulesPath, []byte(combined), 0644); err != nil {
		return err
	}
	if err := storage.AtomicWritePerm(netfilter6MangleRulesPath, []byte(mangle), 0644); err != nil {
		return err
	}
	return storage.AtomicWritePerm(netfilter6NatRulesPath, []byte(nat), 0644)
}

func (systemV6) PersistHook() error {
	if err := storage.AtomicWritePerm(netfilter6HookPath(), []byte(netfilter6HookScript()), 0755); err != nil {
		return err
	}
	return storage.AtomicWritePerm(netfilterCtClean6Path, []byte(ctClean6Script()), 0755)
}

// RemoveFiles: после этого перестройка файрвола NDMS больше не возвращает
// v6-перехват.
func (systemV6) RemoveFiles() {
	_ = os.Remove(netfilter6RulesPath)
	_ = os.Remove(netfilter6MangleRulesPath)
	_ = os.Remove(netfilter6NatRulesPath)
	_ = os.Remove(netfilter6HookPath())
	_ = os.Remove(netfilterCtClean6Path)
}

// CleanFlows best-effort: скрипт сам пишет в syslog, а отказ не должен ронять
// установку — перехват уже поднят.
func (systemV6) CleanFlows(ctx context.Context) {
	_, _ = sysexec.Run(ctx, netfilterCtClean6Path)
}

func (systemV6) PersistBlackhole(input string) error {
	return storage.AtomicWritePerm(netfilter6BlackholePath, []byte(input), 0644)
}

func (systemV6) RemoveBlackholeFile() { _ = os.Remove(netfilter6BlackholePath) }

// Install6 ставит v6-правила: снять прежние джампы, применить, добавить
// правило и маршрут, записать файлы для хука, вычистить старые потоки.
//
// Файлы правил и хук пишутся ПОСЛЕ успешного restore. В обратном порядке
// отвергнутый ядром спек остался бы на диске, и хук применял бы его снова на
// каждой перестройке файрвола. И после правила с маршрутом: если запись на
// диск не удалась, перехват уже работает целиком (без них TPROXY-пакеты
// некуда доставить), а ошибка уходит наверх и установка повторится.
//
// После ошибки состояние ядра неизвестно: restore коммитит по таблицам, и
// часть правил могла примениться. Вызывающий обязан это помнить — файлов на
// диске, по которым снос нашёл бы слой, при этом может не быть.
func (it *IPTables) Install6(ctx context.Context, spec V6Spec) error {
	it.removeJumps6(ctx)
	mangle := buildMangleRestoreInput6(spec)
	nat := buildNatRestoreInput6(spec)
	if err := it.v6.Restore(ctx, mangle+nat); err != nil {
		return fmt.Errorf("ip6tables-restore: %w", err)
	}
	it.drainFwmarkRules6(ctx)
	if err := it.runIP(ctx, "-6", "rule", "add", "fwmark", fmt.Sprintf("0x%x", Fwmark),
		"table", fmt.Sprintf("%d", RoutingTable),
		"priority", fmt.Sprintf("%d", IPRule6Priority)); err != nil {
		if !strings.Contains(err.Error(), "File exists") {
			return fmt.Errorf("ip -6 rule add: %w", err)
		}
	}
	if err := it.runIP(ctx, "-6", "route", "add", "local", "::/0", "dev", "lo",
		"table", fmt.Sprintf("%d", RoutingTable)); err != nil {
		if !strings.Contains(err.Error(), "File exists") {
			return fmt.Errorf("ip -6 route add: %w", err)
		}
	}
	if err := it.v6.PersistRules(mangle+nat, mangle, nat); err != nil {
		return fmt.Errorf("write v6 netfilter rules: %w", err)
	}
	if err := it.v6.PersistHook(); err != nil {
		return fmt.Errorf("write v6 netfilter hook: %w", err)
	}
	// Потоки, родившиеся до правил, идут мимо них — см. ctClean6Script.
	it.v6.CleanFlows(ctx)
	return nil
}

// Uninstall6 сносит v6-слой целиком. Идемпотентно и без ошибок — как
// Uninstall.
func (it *IPTables) Uninstall6(ctx context.Context) {
	it.v6.RemoveFiles()
	it.RemoveBlackhole6(ctx)
	it.removeJumps6(ctx)
	_ = it.v6.Run(ctx, "-t", "mangle", "-F", Chain6Name)
	_ = it.v6.Run(ctx, "-t", "mangle", "-X", Chain6Name)
	_ = it.v6.Run(ctx, "-t", "nat", "-F", Redirect6Chain)
	_ = it.v6.Run(ctx, "-t", "nat", "-X", Redirect6Chain)
	it.drainFwmarkRules6(ctx)
	// del, а не flush: таблица 100 в IPv6 содержит ровно один наш маршрут, а
	// `ip -6 route flush table N` BusyBox-овский ip не умеет.
	_ = it.runIP(ctx, "-6", "route", "del", "local", "::/0", "dev", "lo",
		"table", fmt.Sprintf("%d", RoutingTable))
}

// InstallBlackhole6 поднимает fail-closed блокировку IPv6 — зеркало
// InstallBlackhole: снять прежний джамп, применить, записать правила (по ним
// DEAD-ветка хука вернёт блокировку после перестройки файрвола, пока движок
// мёртв). Ни правила маршрутизации, ни fwmark: блокировка только дропает.
// Файл и хук — после успешного restore, по той же причине, что в Install6.
func (it *IPTables) InstallBlackhole6(ctx context.Context, spec V6Spec) error {
	it.removeJumps6FromTable(ctx, "mangle", Blackhole6Chain)
	input := buildBlackholeRestoreInput6(spec)
	if err := it.v6.Restore(ctx, input); err != nil {
		return fmt.Errorf("ip6tables-restore blackhole: %w", err)
	}
	if err := it.v6.PersistBlackhole(input); err != nil {
		return fmt.Errorf("write v6 blackhole rules: %w", err)
	}
	// Хук обязан существовать, иначе после перестройки файрвола блокировку
	// вернуть некому. Обычно его уже записал Install6.
	if err := it.v6.PersistHook(); err != nil {
		return fmt.Errorf("write v6 netfilter hook: %w", err)
	}
	return nil
}

// RemoveBlackhole6 снимает блокировку: файл правил (чтобы хук перестал её
// возвращать), джамп, затем цепочку. Идемпотентно.
func (it *IPTables) RemoveBlackhole6(ctx context.Context) {
	it.v6.RemoveBlackholeFile()
	it.removeJumps6FromTable(ctx, "mangle", Blackhole6Chain)
	_ = it.v6.Run(ctx, "-t", "mangle", "-F", Blackhole6Chain)
	_ = it.v6.Run(ctx, "-t", "mangle", "-X", Blackhole6Chain)
}

func (it *IPTables) drainFwmarkRules6(ctx context.Context) {
	for i := 0; i < maxIPRuleDrainPasses; i++ {
		if err := it.runIP(ctx, "-6", "rule", "del", "fwmark", fmt.Sprintf("0x%x", Fwmark),
			"table", fmt.Sprintf("%d", RoutingTable)); err != nil {
			break
		}
	}
}

func (it *IPTables) removeJumps6(ctx context.Context) {
	it.removeJumps6FromTable(ctx, "mangle", Chain6Name)
	it.removeJumps6FromTable(ctx, "nat", Redirect6Chain)
	// Self-loop DROP в mangle INPUT: restore идёт с --noflush, без сноса копии
	// копились бы на каждом Install6.
	removeCommentTaggedRules(ctx, it.v6.RunOutput, it.v6.Run, "mangle", "INPUT", SelfLoopTag)
}

func (it *IPTables) removeJumps6FromTable(ctx context.Context, table, chain string) {
	out, err := it.v6.RunOutput(ctx, "-t", table, "-S", "PREROUTING")
	if err != nil {
		return
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "-A PREROUTING ") || !jumpToken(line, chain) {
			continue
		}
		args := append([]string{"-t", table}, strings.Fields(strings.Replace(line, "-A PREROUTING", "-D PREROUTING", 1))...)
		_ = it.v6.Run(ctx, args...)
	}
}

// v6State — живое состояние v6-слоя, по одному `-S` на таблицу.
type v6State struct {
	// anyChain — объявлена хотя бы одна наша цепочка.
	anyChain bool
	// jumps — обе цепочки объявлены и входятся из PREROUTING.
	jumps bool
	// blackhole — цепочка блокировки жива И входится. Как и в v4, цепочка без
	// джампа ничего не дропает — это fail-OPEN.
	blackhole bool
}

// probe6 снимает состояние v6-слоя. На ошибке запроса нули означают
// «неизвестно», а не «отсутствует» — тот же контракт, что у probeAll.
func (it *IPTables) probe6(ctx context.Context) (v6State, error) {
	mangle, err := it.v6.RunOutput(ctx, "-t", "mangle", "-S")
	if err != nil {
		return v6State{}, err
	}
	nat, err := it.v6.RunOutput(ctx, "-t", "nat", "-S")
	if err != nil {
		return v6State{}, err
	}
	mChain, mJump := scanChain(mangle, Chain6Name)
	nChain, nJump := scanChain(nat, Redirect6Chain)
	bChain, bJump := scanChain(mangle, Blackhole6Chain)
	return v6State{
		anyChain:  mChain || nChain,
		jumps:     mChain && nChain && mJump && nJump,
		blackhole: bChain && bJump,
	}, nil
}

// ---- файлы и хук ---------------------------------------------------------

// blackhole6FilePresent — долговечная половина блокировки: файл переживает и
// ALIVE-ветку хука, и рестарт демона (см. blackholeRulesFilePresent).
var blackhole6FilePresent = func() bool {
	_, err := os.Stat(netfilter6BlackholePath)
	return err == nil
}

// v6RulesFilePresent — долговечный признак «v6-слой был установлен»: файл
// переживает рестарт демона, в отличие от снимка спека в памяти.
var v6RulesFilePresent = func() bool {
	_, err := os.Stat(netfilter6RulesPath)
	return err == nil
}

// netfilter6HookScript рендерит хук v6-слоя — зеркало netfilterHookScript.
// Pure (без I/O): тест проверяет шелл через `sh -n`.
//
// Две взаимоисключающие ветки, по тому, жив ли sing-box:
//
//   - ALIVE: правит настоящий перехват. Сначала сносится fail-closed
//     блокировка, если задержалась, затем возвращаются цепочки перехвата, у
//     которых NDMS стёр PREROUTING-джампы, — только та таблица, которую
//     перестроили. После восстановления mangle запускается чистка потоков
//     (ctClean6Script).
//   - DEAD: fail-closed. Возвращается блокировка (если есть её файл правил, а
//     джамп стёрт), чтобы трафик политики не ушёл мимо sing-box, пока движок
//     лежит. Перехват в мёртвый порт здесь не восстанавливается — как и в v4.
//
// Проверка $type инвертирована («не iptables»), как и в остальных хуках: при
// пустом $type положительная проверка молча убила бы хук.
//
// Набор AWGM-BYPASS6 восстанавливается ДО любого ip6tables-restore по той же
// причине и тем же приёмом, что AWGM-BYPASS в v4-хуке: правило с `-m set`
// роняет весь restore (включая блокировку), пока набора нет, а наборы живут в
// RAM. Гейт — успех create БЕЗ -exist: он проходит только когда набора не
// было.
func netfilter6HookScript() string {
	return fmt.Sprintf(`#!/bin/sh
[ "$type" = "iptables" ] && exit 0
case "$table" in mangle|nat) ;; *) exit 0 ;; esac
IP6T=%[2]s
[ -x "$IP6T" ] || exit 0
KREL="$(uname -r)"
for mod in xt_TPROXY xt_mark xt_conntrack xt_dscp xt_set; do
  grep -q "^${mod} " /proc/modules 2>/dev/null && continue
  [ -f "/lib/modules/${KREL}/${mod}.ko" ] && insmod "/lib/modules/${KREL}/${mod}.ko" 2>/dev/null || true
done
if [ -f %[12]q ]; then
  if /opt/sbin/ipset create %[13]s hash:net maxelem %[14]d family inet6 2>/dev/null; then
    /opt/sbin/ipset restore -exist < %[12]q
  fi
fi
# scrub_jumps <table> <chain>: удалить все PREROUTING-джампы в <chain>.
scrub_jumps() {
  "$IP6T" -w -t "$1" -S PREROUTING 2>/dev/null \
    | grep -E -- "-[jg] $2(\$| )" \
    | sed 's/-A PREROUTING/-D PREROUTING/' \
    | while IFS= read -r line; do "$IP6T" -w -t "$1" $line 2>/dev/null; done
}
# scrub_tagged <table> <tag> <chain>: удалить правила с комментарием <tag>.
scrub_tagged() {
  "$IP6T" -w -t "$1" -S "$3" 2>/dev/null \
    | grep -E -- "--comment \"?$2" \
    | sed "s/-A $3/-D $3/" \
    | while IFS= read -r line; do "$IP6T" -w -t "$1" $line 2>/dev/null; done
}
# table_ok <table> <chain>: цепочка существует И в неё входят из PREROUTING.
table_ok() {
  "$IP6T" -w -t "$1" -nL "$2" >/dev/null 2>&1 \
    && "$IP6T" -w -t "$1" -S PREROUTING 2>/dev/null | grep -qE -- "-[jg] $2(\$| )"
}
if pidof sing-box >/dev/null 2>&1; then
  # sing-box жив — блокировку снимаем, чтобы её DROP не стоял перед перехватом.
  scrub_jumps mangle %[15]s
  "$IP6T" -w -t mangle -F %[15]s 2>/dev/null
  "$IP6T" -w -t mangle -X %[15]s 2>/dev/null
  [ -f %[1]q ] || exit 0
  if [ "$table" = "mangle" ] && ! table_ok mangle %[3]s && [ -f %[5]q ]; then
    scrub_jumps mangle %[3]s
    scrub_tagged mangle %[18]s INPUT
    %[7]s --noflush < %[5]q
    %[8]s -6 rule del fwmark 0x%[9]x table %[10]d 2>/dev/null
    %[8]s -6 rule add fwmark 0x%[9]x table %[10]d priority %[11]d 2>/dev/null || true
    %[8]s -6 route add local ::/0 dev lo table %[10]d 2>/dev/null || true
    logger -t awgm-tproxy6 "netfilter.d: restored mangle %[3]s after NDMS reload"
    [ -x %[17]q ] && %[17]q
  fi
  if [ "$table" = "nat" ] && ! table_ok nat %[4]s && [ -f %[6]q ]; then
    scrub_jumps nat %[4]s
    %[7]s --noflush < %[6]q
    logger -t awgm-tproxy6 "netfilter.d: restored nat %[4]s after NDMS reload"
  fi
else
  # sing-box мёртв — fail-closed: возвращаем блокировку, если она была
  # поставлена (есть файл правил), а её джамп стёрт.
  [ -f %[16]q ] || exit 0
  if ! "$IP6T" -w -t mangle -S PREROUTING 2>/dev/null | grep -qE -- '-[jg] %[15]s($| )'; then
    %[7]s --noflush < %[16]q
    logger -t awgm-tproxy6 "netfilter.d: re-asserted fail-closed blackhole (sing-box down)"
  fi
fi
exit 0
`, netfilter6RulesPath, sysiptables.Binary6, Chain6Name, Redirect6Chain,
		netfilter6MangleRulesPath, netfilter6NatRulesPath, sysiptables.RestoreBinary6,
		ipBinary, Fwmark, RoutingTable, IPRule6Priority,
		bypassSave6Path, bypassset.SetName6, bypassset.SetMaxElem,
		Blackhole6Chain, netfilter6BlackholePath, netfilterCtClean6Path, SelfLoopTag)
}

// conntrackBinary — conntrack-tools из Entware; тот же путь, что в ctclean.
const conntrackBinary = "/opt/sbin/conntrack"

// ppeFlushPath — узел сброса таблицы аппаратного ускорения MTK PPE; тот же,
// что в awgm-ctclean.sh (#684). На платформах без PPE его нет.
const ppeFlushPath = "/proc/sys/net/hwnat/ppe_flush"

// ctClean6Script рендерит скрипт чистки потоков v6-слоя — зеркало
// ctCleanScript. Его зовут Install6 и хук после восстановления mangle. Pure
// (без I/O): тест проверяет шелл через `sh -n`.
//
// Что чистится и зачем:
//
//   - Таблица аппаратного ускорения. Ускорение IPv6 на Keenetic включено
//     (hwnat/ipv6_offload), и соединения, установленные до правил, идут мимо
//     netfilter: ни новые правила, ни снос записи conntrack их не возвращают.
//     Сбрасываем только когда джамп TPROXY действительно на месте — иначе
//     потоки переучились бы через ту же дыру. v4-скрипт делает то же, но до
//     установки v6-правил, поэтому слой сбрасывает сам.
//   - UDP-записи conntrack потоков, родившихся до правил. Маршрут политики мог
//     вести их через NAT, и TPROXY доставляет их пакеты уже с подменённым
//     источником — sing-box отвечает не тому. Следующий пакет создаст поток
//     заново, уже через правила. TCP не трогаем по той же причине, что и v4:
//     обрыв установленного соединения хуже, а новые перехватятся сами.
//
// Режим читается из живого джампа, как в v4:
//
//   - политика (в джампе есть метка) — один вызов `conntrack -D --mark`:
//     NDMS сохраняет метку политики в connmark, отбор идёт в ядре;
//   - «все устройства» — проход по /proc/net/nf_conntrack с отбором по полю
//     mac=. Keenetic пишет его на каждом потоке, начатом устройством LAN, и не
//     пишет на собственных потоках роутера и на соединениях, открытых снаружи,
//     — так под снос попадают ровно перехватываемые потоки.
func ctClean6Script() string {
	return fmt.Sprintf(`#!/bin/sh
IP6T=%[1]s
jumps="$("$IP6T" -w -t mangle -S PREROUTING 2>/dev/null | grep -E -- '-[jg] %[2]s($| )')"
[ -n "$jumps" ] || exit 0
if [ -w %[3]s ] && echo 1 > %[3]s 2>/dev/null; then
  logger -t awgm-ctclean6 "flushed PPE offload table after TPROXY restore"
fi
CT=%[4]s
[ -x "$CT" ] || { logger -t awgm-ctclean6 "conntrack tool missing — stale-flow eviction skipped"; exit 0; }
pmark="$(printf '%%s\n' "$jumps" | sed -n 's/.*-m mark --mark \(0x[0-9a-fA-F]*\).*/\1/p' | head -n 1)"
[ -n "$pmark" ] && pmark="$(printf '%%d' "$pmark" 2>/dev/null)"
n=0
if [ -n "$pmark" ]; then
  d="$("$CT" -D -f ipv6 -p udp --mark "$pmark" 2>&1 >/dev/null \
    | sed -n 's/.* \([0-9][0-9]*\) flow entries.*/\1/p')"
  [ -n "$d" ] && n="$d"
else
  list="$(awk '
    $1 == "ipv6" && $3 == "udp" {
      src = ""; dst = ""; sp = ""; dp = ""; hasmac = 0;
      for (i = 1; i <= NF; i++) {
        if ($i ~ /^src=/ && src == "") src = substr($i, 5);
        else if ($i ~ /^dst=/ && dst == "") dst = substr($i, 5);
        else if ($i ~ /^sport=/ && sp == "") sp = substr($i, 7);
        else if ($i ~ /^dport=/ && dp == "") dp = substr($i, 7);
        else if ($i ~ /^mac=/) hasmac = 1;
      }
      if (hasmac && src != "" && dst != "" && sp != "" && dp != "") print src, dst, sp, dp;
    }' /proc/net/nf_conntrack 2>/dev/null)"
  if [ -n "$list" ]; then
    n="$(printf '%%s\n' "$list" | wc -l)"
    printf '%%s\n' "$list" | while read -r s d sp dp; do
      "$CT" -D -f ipv6 -p udp -s "$s" -d "$d" --sport "$sp" --dport "$dp" >/dev/null 2>&1
    done
  fi
fi
[ "$n" -gt 0 ] && logger -t awgm-ctclean6 "evicted $n IPv6 UDP flow(s) after TPROXY restore"
exit 0
`, sysiptables.Binary6, Chain6Name, ppeFlushPath, conntrackBinary)
}
