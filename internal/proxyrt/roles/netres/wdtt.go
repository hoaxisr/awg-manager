package netres

// Построители групп WDTT-сервера. Форма правила — часть контракта, а не
// стиль: движок сверяет живое правило со своим по строке и усыновляет его,
// поэтому любое расхождение с тем, что УЖЕ стоит на роутерах пользователей,
// даёт вторую копию рядом вместо усыновления. Отсюда побайтовое совпадение
// со строками, которые ставил прежний код (entware_nat_linux.go,
// server_raw_policy_linux.go), — довод про установленную базу, не про то,
// что старый код был прав.

// Comment — метка владения общих правил (nat/POSTROUTING). FORWARD accept и
// цепочка awgm_wdtt_mangle меток не несут: правило адресовано НАШЕМУ
// интерфейсу, имя интерфейса и есть признак владения (памятка проекта).
const Comment = "AWGM_WDTT"

// MSSChain — своя цепочка clamp-правил.
const MSSChain = "awgm_wdtt_mangle"

// HookPath — путь netfilter.d-хука (старый, усыновляется).
const HookPath = "/opt/etc/ndm/netfilter.d/61-awgm-wdtt-forward.sh"

// ForwardGroups — FORWARD accept по -i и -o для каждого интерфейса.
func ForwardGroups(ifaces []string) []Group {
	var out []Group
	for _, iface := range ifaces {
		if iface == "" {
			continue
		}
		out = append(out, Group{Guard: iface, Rules: []Rule{
			{Chain: "FORWARD", Pos: 1, Spec: []string{"-i", iface, "-j", "ACCEPT"}},
			{Chain: "FORWARD", Pos: 1, Spec: []string{"-o", iface, "-j", "ACCEPT"}},
		}})
	}
	return out
}

// MasqPlan — kernel-iface + CIDR клиентов (паритет entwareNATPlan).
type MasqPlan struct {
	Iface string
	CIDR  string
}

// MasqGroups — SNAT. none: правил нет вовсе. full: `-s CIDR ! -o iface` (NAT на любом egress:
// fwmark-таблицы шлют клиентов в разные интерфейсы, PR #697 F8);
// internet-only: жёсткий `-o staticWANDev`. Пустой staticWANDev при
// internet-only — дефект вызывающего (молчаливая деградация в full-форму
// была багом H1, PR #697): построитель отдаёт nil, но первичный гейт живёт
// выше — Validate конфига отклоняет internet-only без WAN (задача 1), а
// провайдер natGroups (задача 10) превращает остаточный случай (дрейф
// конфига между прогонами) в ошибку наблюдения (Unknown), не в full-форму.
func MasqGroups(plans []MasqPlan, mode, staticWANDev string) []Group {
	var out []Group
	for _, p := range plans {
		var spec []string
		switch {
		case mode == "none":
			// Режим значит «без подмены адреса источника» — маскарада нет по
			// определению. Своя ветка обязательна: без неё none попадал в
			// default и получал ПОЛНУЮ форму, то есть ровно то, что режим
			// запрещает.
			return nil
		case mode == "internet-only" && staticWANDev != "":
			spec = []string{"-s", p.CIDR, "-o", staticWANDev,
				"-m", "comment", "--comment", Comment, "-j", "MASQUERADE"}
		case mode == "internet-only":
			return nil
		default:
			spec = []string{"-s", p.CIDR, "!", "-o", p.Iface,
				"-m", "comment", "--comment", Comment, "-j", "MASQUERADE"}
		}
		out = append(out, Group{Guard: p.Iface, Rules: []Rule{
			{Table: "nat", Chain: "POSTROUTING", Pos: 1, Spec: spec},
		}})
	}
	return out
}

// DNSHijack — DNAT :53 на шлюз, который видят клиенты (PR #697, F1).
type DNSHijack struct {
	Iface   string
	Gateway string
}

func DNSGroups(specs []DNSHijack) []Group {
	var out []Group
	for _, d := range specs {
		g := Group{Guard: d.Iface}
		for _, proto := range []string{"udp", "tcp"} {
			g.Rules = append(g.Rules,
				Rule{Chain: "INPUT", Pos: 1,
					Spec: []string{"-i", d.Iface, "-p", proto, "--dport", "53", "-j", "ACCEPT"}},
				Rule{Table: "nat", Chain: "PREROUTING", Pos: 1,
					Spec: []string{"-i", d.Iface, "-p", proto, "--dport", "53",
						"-j", "DNAT", "--to-destination", d.Gateway + ":53"}},
			)
		}
		out = append(out, g)
	}
	return out
}

// MSSRules — clamp в СВОЕЙ цепочке (append, без позиций).
func MSSRules(cidrs []string) []Rule {
	var out []Rule
	for _, cidr := range cidrs {
		for _, dir := range []string{"-s", "-d"} {
			out = append(out, Rule{Table: "mangle", Chain: MSSChain,
				Spec: []string{dir, cidr, "-p", "tcp", "--tcp-flags", "SYN,RST", "SYN",
					"-j", "TCPMSS", "--clamp-mss-to-pmtu"}})
		}
	}
	return out
}
