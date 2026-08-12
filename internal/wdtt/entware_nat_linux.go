//go:build linux

package wdtt

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/sys/exec"
	"github.com/hoaxisr/awg-manager/internal/sys/iptables"
)

const (
	entwareNATComment            = "AWGM_WDTT"
	wdttForwardNetfilterHookPath = "/opt/etc/ndm/netfilter.d/61-awgm-wdtt-forward.sh"
)

// entwareNATPresentForServer reports whether all planned NAT/FORWARD rules exist.
func entwareNATPresentForServer(ctx context.Context, cfg ServerConfig, mode, wanDev string) bool {
	plans := cfg.serverEntwareNATPlansForMode(mode)
	if len(plans) == 0 || normalizeNatMode(mode) == "none" {
		return true
	}
	ifaces := entwarePlansIfaces(plans)
	cidrs := entwarePlansCIDRs(plans)
	natOut, err1 := iptables.RunOutput(ctx, "-t", "nat", "-S", "POSTROUTING")
	fwdOut, err2 := iptables.RunOutput(ctx, "-S", "FORWARD")
	if err1 != nil || err2 != nil {
		return false
	}
	if !strings.Contains(fwdOut, entwareNATComment) && !entwareForwardIfacesPresent(fwdOut, ifaces) {
		return false
	}
	for _, iface := range ifaces {
		if !strings.Contains(fwdOut, iface) && !entwareForwardIfacesPresent(fwdOut, []string{iface}) {
			return false
		}
	}
	for _, cidr := range cidrs {
		if !strings.Contains(natOut, cidr) {
			return false
		}
	}
	if wanDev == "" {
		dev, err := defaultWANDev(ctx)
		if err != nil {
			return strings.Contains(natOut, entwareNATComment)
		}
		wanDev = dev
	}
	for _, plan := range plans {
		want := strings.Join(masqueradeMatchArgs(plan, mode, wanDev), " ")
		found := false
		for _, line := range strings.Split(natOut, "\n") {
			if strings.Contains(line, want) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// masqueradeMatchArgs — match-часть SNAT-правила для одного плана.
// full: `-s CIDR ! -o <client-iface>` — NAT на любом egress: fwmark-таблицы
// (HR, политики) шлют клиентов в разные интерфейсы, привязка к одному -o
// оставляла остальные пути без SNAT (PR #697, F8).
// ponytail: клиент→LAN тоже маскарадится (br0) — осознанно: LAN-устройствам
// не нужен маршрут в клиентский пул; убрать, если понадобятся честные src.
// internet-only: жёсткий `-o <staticWAN>` — NAT только в выбранный WAN.
func masqueradeMatchArgs(plan entwareNATPlan, mode, staticWANDev string) []string {
	if normalizeNatMode(mode) == "internet-only" && strings.TrimSpace(staticWANDev) != "" {
		return []string{"-s", plan.CIDR, "-o", staticWANDev,
			"-m", "comment", "--comment", entwareNATComment, "-j", "MASQUERADE"}
	}
	return []string{"-s", plan.CIDR, "!", "-o", plan.Iface,
		"-m", "comment", "--comment", entwareNATComment, "-j", "MASQUERADE"}
}

func applyEntwareNATForServer(ctx context.Context, cfg ServerConfig, mode, wanDev, rawMark string) error {
	mode = normalizeNatMode(mode)
	plans := cfg.serverEntwareNATPlansForMode(mode)
	activeIfaces := entwarePlansIfaces(plans)
	activeCIDRs := entwarePlansCIDRs(plans)
	if len(plans) == 0 {
		return nil
	}
	if mode == "none" {
		// Живые вызовы (access.go applyServerAccess, nat_reconcile.go) отсекают
		// mode=="none" ДО этой функции и сами зовут removeWdttForwardNetfilterHook —
		// эта ветка defensive, на случай будущих call-site'ов без такого отсева.
		removeEntwareNATForServer(ctx, cfg)
		removeWdttForwardNetfilterHook()
		return nil
	}
	// Убрать entware с ifaces, которые больше не в плане (OpkgTun → NDMS для WG).
	for _, iface := range cfg.serverEntwareNATIfaces() {
		still := false
		for _, a := range activeIfaces {
			if a == iface {
				still = true
				break
			}
		}
		if !still {
			removeEntwareNATIfaces(ctx, iface)
		}
	}
	extIface, err := resolveExtIfaceOrDefault(ctx, wanDev)
	if err != nil && mode == "internet-only" {
		return err
	}
	_ = os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0o644)

	if err := setupEntwareForward(ctx, activeIfaces...); err != nil {
		return fmt.Errorf("entware FORWARD: %w", err)
	}
	if err := ensureWdttNetfilterHook(ctx, wdttNetfilterSpecForServer(cfg, mode, extIface, rawMark)); err != nil {
		return fmt.Errorf("netfilter.d hook: %w", err)
	}
	setupEntwareMSSClamp(ctx, activeCIDRs...)

	flushEntwareMasquerade(ctx)
	for _, plan := range plans {
		args := append([]string{"-t", "nat", "-I", "POSTROUTING", "1"},
			masqueradeMatchArgs(plan, mode, extIface)...)
		if err := iptables.Run(ctx, args...); err != nil {
			return fmt.Errorf("MASQUERADE %s: %w", plan.CIDR, err)
		}
	}
	return nil
}

func removeEntwareNATForServer(ctx context.Context, cfg ServerConfig) {
	removeEntwareNATIfaces(ctx, cfg.serverEntwareNATIfaces()...)
}

// wdttDNSSpec — kernel iface WDTT-сервера + gateway для DNAT :53.
type wdttDNSSpec struct {
	Iface   string
	Gateway string
}

// wdttNetfilterSpec — вход генератора netfilter.d-хука: правила, переживающие
// перезапись таблиц NDM (filter/nat/mangle), в одном скрипте с диспетчером по $table.
type wdttNetfilterSpec struct {
	ForwardIfaces []string         // filter: FORWARD accept
	DNS           []wdttDNSSpec    // filter: INPUT :53 accept; nat: DNAT :53 → Gateway
	Masq          []entwareNATPlan // nat: MASQUERADE (masqueradeMatchArgs)
	MasqMode      string           // full | internet-only
	MasqStaticWAN string           // для internet-only
	RawPolicyMark string           // mangle: MARK+CONNMARK на wdttraw0; "" — не ставить
}

// wdttNetfilterSpecForServer собирает spec для cfg/mode/wanDev/rawMark —
// единая точка сборки для applyEntwareNATForServer и nat-reconcile.
func wdttNetfilterSpecForServer(cfg ServerConfig, mode, wanDev, rawMark string) wdttNetfilterSpec {
	plans := cfg.serverEntwareNATPlansForMode(mode)
	spec := wdttNetfilterSpec{
		ForwardIfaces: entwarePlansIfaces(plans),
		Masq:          plans,
		MasqMode:      mode,
		MasqStaticWAN: wanDev,
		RawPolicyMark: rawMark,
		DNS:           []wdttDNSSpec{{Iface: DefaultRawServerIface, Gateway: DefaultRawServerAddr}},
	}
	if cfg.UsesWireGuardRelay() {
		spec.DNS = append(spec.DNS, wdttDNSSpec{Iface: cfg.kernelServerIface(), Gateway: cfg.serverAccessAddress()})
	}
	return spec
}

func wdttNetfilterHookScript(spec wdttNetfilterSpec) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# AWG Manager: правила WDTT-сервера, переживающие перезапись таблиц NDM.\n")
	b.WriteString("# filter: FORWARD/INPUT; nat: DNAT :53 + MASQUERADE; mangle: policy-mark.\n")
	b.WriteString("[ \"$type\" = \"ip6tables\" ] && exit 0\n")
	b.WriteString("IPTABLES=/opt/sbin/iptables\n")
	b.WriteString("[ -x \"$IPTABLES\" ] || IPTABLES=iptables\n")
	b.WriteString("run() { \"$IPTABLES\" -w \"$@\" 2>/dev/null || \"$IPTABLES\" \"$@\" 2>/dev/null; }\n")
	b.WriteString("has_if() { /opt/sbin/ip link show \"$1\" >/dev/null 2>&1; }\n")
	forwardIfaces := dedupeStrings(spec.ForwardIfaces)
	sort.Strings(forwardIfaces)
	b.WriteString("case \"$table\" in\nfilter)\n")
	for _, iface := range forwardIfaces {
		fmt.Fprintf(&b, "if has_if %q; then\n", iface)
		// Форма — из entwareForwardMatch: хук и Go-код обязаны ставить,
		// проверять и сносить одно и то же правило.
		for _, dir := range []string{"-i", "-o"} {
			match := strings.Join(entwareForwardMatch(dir, fmt.Sprintf("%q", iface)), " ")
			fmt.Fprintf(&b, "  run -C FORWARD %s || run -I FORWARD 1 %s\n", match, match)
		}
		b.WriteString("fi\n")
	}
	for _, d := range spec.DNS {
		fmt.Fprintf(&b, "if has_if %q; then\n", d.Iface)
		for _, proto := range []string{"udp", "tcp"} {
			rule := fmt.Sprintf("INPUT 1 -i %q -p %s --dport 53 -j ACCEPT", d.Iface, proto)
			check := fmt.Sprintf("INPUT -i %q -p %s --dport 53 -j ACCEPT", d.Iface, proto)
			fmt.Fprintf(&b, "  run -C %s || run -I %s\n", check, rule)
		}
		b.WriteString("fi\n")
	}
	b.WriteString(";;\nnat)\n")
	for _, d := range spec.DNS {
		fmt.Fprintf(&b, "if has_if %q; then\n", d.Iface)
		for _, proto := range []string{"udp", "tcp"} {
			match := fmt.Sprintf("-i %q -p %s --dport 53 -j DNAT --to-destination %s:53", d.Iface, proto, d.Gateway)
			fmt.Fprintf(&b, "  run -t nat -C PREROUTING %s || run -t nat -I PREROUTING 1 %s\n", match, match)
		}
		b.WriteString("fi\n")
	}
	for _, plan := range spec.Masq {
		match := strings.Join(masqueradeMatchArgs(plan, spec.MasqMode, spec.MasqStaticWAN), " ")
		fmt.Fprintf(&b, "if has_if %q; then\n", plan.Iface)
		fmt.Fprintf(&b, "  run -t nat -C POSTROUTING %s || run -t nat -I POSTROUTING 1 %s\n", match, match)
		b.WriteString("fi\n")
	}
	b.WriteString(";;\nmangle)\n")
	if mark := strings.TrimSpace(spec.RawPolicyMark); mark != "" {
		iface := DefaultRawServerIface
		connCheck := fmt.Sprintf("-t mangle -C PREROUTING -i %q -j CONNMARK --save-mark --nfmask 0xffffffff --ctmask 0xffffffff", iface)
		markCheck := fmt.Sprintf("-t mangle -C PREROUTING -i %q -j MARK --set-xmark %s/0xffffffff", iface, mark)
		connIns := fmt.Sprintf("-t mangle -I PREROUTING 1 -i %q -j CONNMARK --save-mark --nfmask 0xffffffff --ctmask 0xffffffff", iface)
		markIns := fmt.Sprintf("-t mangle -I PREROUTING 1 -i %q -j MARK --set-xmark %s/0xffffffff", iface, mark)
		fmt.Fprintf(&b, "if has_if %q; then\n", iface)
		// Пара CONNMARK+MARK вставляется ТОЛЬКО когда ОБА правила отсутствуют:
		// независимая довставка при частичном состоянии (одно есть, другого
		// нет) инвертирует итоговый порядок в цепочке (баг F3, PR #697, чинил
		// коммит a0066f9b). Частичное состояние — забота Go-reconcile:
		// rawServerPolicyMarkPresent находит рассинхрон и пересобирает оба
		// правила в правильном порядке за ≤ natReconcileInterval (15с).
		fmt.Fprintf(&b, "  if ! run %s && ! run %s; then\n", connCheck, markCheck)
		fmt.Fprintf(&b, "    run %s\n", connIns)
		fmt.Fprintf(&b, "    run %s\n", markIns)
		b.WriteString("  fi\n")
		b.WriteString("fi\n")
	}
	b.WriteString(";;\nesac\nexit 0\n")
	return b.String()
}

func ensureWdttNetfilterHook(ctx context.Context, spec wdttNetfilterSpec) error {
	spec.ForwardIfaces = dedupeStrings(spec.ForwardIfaces)
	if len(spec.ForwardIfaces) == 0 {
		return nil
	}
	script := wdttNetfilterHookScript(spec)
	if err := storage.AtomicWritePerm(wdttForwardNetfilterHookPath, []byte(script), 0o755); err != nil {
		return err
	}
	for _, table := range []string{"filter", "nat", "mangle"} {
		if _, err := exec.Run(ctx, "sh", "-c",
			"table="+table+" type=iptables sh "+wdttForwardNetfilterHookPath); err != nil {
			return err
		}
	}
	return nil
}

func removeWdttForwardNetfilterHook() {
	_ = os.Remove(wdttForwardNetfilterHookPath)
}

func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func removeEntwareNAT(ctx context.Context, wgIface string) {
	removeEntwareNATIfaces(ctx, wgIface)
}

func removeEntwareNATIfaces(ctx context.Context, ifaces ...string) {
	flushEntwareMasquerade(ctx)
	removeEntwareMSSClamp(ctx)
	for _, iface := range ifaces {
		iface = strings.TrimSpace(iface)
		if iface == "" {
			continue
		}
		for i := 0; i < 5; i++ {
			for _, dir := range []string{"-i", "-o"} {
				for _, spec := range entwareForwardDeleteSpecs(dir, iface) {
					_ = iptables.Run(ctx, append([]string{"-D", "FORWARD"}, spec...)...)
				}
			}
		}
		removeWdttDNSRules(ctx, iface)
	}
}

// removeWdttDNSRules сносит DNAT :53 (nat/PREROUTING) и INPUT :53 (filter/INPUT)
// для iface — по образцу removeRawServerPolicyMarkRules: реплей -S, матч по
// --dport 53 + -i <iface>, удаление -D (iptables -D требует точного совпадения
// спеки, фиксированную спеку нельзя удалить вслепую).
func removeWdttDNSRules(ctx context.Context, iface string) {
	type target struct {
		args  []string // -t <table> -S <chain>
		table []string // -t <table> -D
	}
	targets := []target{
		{args: []string{"-t", "nat", "-S", "PREROUTING"}, table: []string{"-t", "nat", "-D"}},
		{args: []string{"-S", "INPUT"}, table: []string{"-D"}},
	}
	for _, tg := range targets {
		for pass := 0; pass < 8; pass++ {
			out, err := iptables.RunOutput(ctx, tg.args...)
			if err != nil {
				break
			}
			var deleted bool
			for _, line := range strings.Split(out, "\n") {
				// Границы токена: без них "-i opkgtun1" ловит и "-i opkgtun17"
				// (см. entwareForwardIfacesPresent в этом же файле).
				hasIface := strings.Contains(line, " -i "+iface+" ") ||
					strings.HasSuffix(strings.TrimSpace(line), " -i "+iface)
				if !hasIface || !strings.Contains(line, "--dport 53") {
					continue
				}
				fields := strings.Fields(line)
				if len(fields) < 2 || fields[0] != "-A" {
					continue
				}
				if iptables.Run(ctx, append(append([]string{}, tg.table...), fields[1:]...)...) == nil {
					deleted = true
					break
				}
			}
			if !deleted {
				break
			}
		}
	}
}

// flushEntwareMasquerade удаляет все AWGM_WDTT MASQUERADE-правила реплеем
// вывода -S: iptables -D требует точного совпадения спеки, а -o меняется при
// смене WAN — удаление по фиксированной спеке молча промахивается.
func flushEntwareMasquerade(ctx context.Context) {
	out, err := iptables.RunOutput(ctx, "-t", "nat", "-S", "POSTROUTING")
	if err != nil {
		return
	}
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, entwareNATComment) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "-A" {
			continue
		}
		_ = iptables.Run(ctx, append([]string{"-t", "nat", "-D"}, fields[1:]...)...)
	}
}

func entwareForwardIfacesPresent(fwdOut string, ifaces []string) bool {
	for _, iface := range ifaces {
		iface = strings.TrimSpace(iface)
		if iface == "" {
			continue
		}
		in := false
		out := false
		for _, line := range strings.Split(fwdOut, "\n") {
			if !strings.Contains(line, iface) {
				continue
			}
			if strings.Contains(line, " -i "+iface+" ") || strings.HasSuffix(strings.TrimSpace(line), " -i "+iface) {
				in = true
			}
			if strings.Contains(line, " -o "+iface+" ") || strings.HasSuffix(strings.TrimSpace(line), " -o "+iface) {
				out = true
			}
		}
		if !in || !out {
			return false
		}
	}
	return len(ifaces) > 0
}

// entwareForwardMatch — спека правила FORWARD accept: одна на вставку, проверку
// и снос. Единая точка не ради красоты: с 2.17.0 эти три формы разъехались
// (вставка и проверка стали голыми, снос остался на помеченной) — и снос
// перестал удалять хоть что-нибудь, оставляя `-i <iface> -j ACCEPT` в FORWARD
// навсегда после каждой остановки сервера.
//
// Метки `-m comment` здесь нет и не нужно: правило адресовано НАШЕМУ интерфейсу
// (wdttraw0 / opkgtunN), чужих правил на нём не бывает — имя интерфейса и есть
// признак владения. Тем же держится и цепочка awgm_wdtt_mangle ниже.
// ponytail: имя интерфейса как признак владения; метка понадобится, только если
// правило когда-нибудь станет адресовать общий ресурс вместо своего интерфейса.
func entwareForwardMatch(dir, ifaceToken string) []string {
	return []string{dir, ifaceToken, "-j", "ACCEPT"}
}

// entwareForwardDeleteSpecs — все формы, которыми мы когда-либо ставили это
// правило: текущая и помеченная из версий ≤2.16.x. Снос обязан покрывать обе,
// иначе апгрейд оставляет старую форму в FORWARD навсегда.
func entwareForwardDeleteSpecs(dir, iface string) [][]string {
	return [][]string{
		entwareForwardMatch(dir, iface),
		{dir, iface, "-m", "comment", "--comment", entwareNATComment, "-j", "ACCEPT"},
	}
}

func entwareForwardRulePresent(ctx context.Context, dir, iface string) bool {
	iface = strings.TrimSpace(iface)
	if iface == "" {
		return true
	}
	switch strings.TrimSpace(dir) {
	case "-i", "-o":
	default:
		return false
	}
	return iptables.Run(ctx, append([]string{"-C", "FORWARD"},
		entwareForwardMatch(dir, iface)...)...) == nil
}

func ensureEntwareForwardRule(ctx context.Context, dir, iface string) error {
	iface = strings.TrimSpace(iface)
	if iface == "" {
		return nil
	}
	if entwareForwardRulePresent(ctx, dir, iface) {
		return nil
	}
	if err := iptables.Run(ctx, append([]string{"-I", "FORWARD", "1"},
		entwareForwardMatch(dir, iface)...)...); err != nil {
		return fmt.Errorf("FORWARD %s %s: %w", dir, iface, err)
	}
	return nil
}

func setupEntwareForward(ctx context.Context, ifaces ...string) error {
	var firstErr error
	for _, wgIface := range ifaces {
		wgIface = strings.TrimSpace(wgIface)
		if wgIface == "" {
			continue
		}
		if err := ensureEntwareForwardRule(ctx, "-i", wgIface); err != nil && firstErr == nil {
			firstErr = err
		}
		if err := ensureEntwareForwardRule(ctx, "-o", wgIface); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

const entwareMSSChain = "awgm_wdtt_mangle"

func entwareMSSPresentAll(ctx context.Context, peerCIDRs []string) bool {
	if len(peerCIDRs) == 0 {
		return true
	}
	out, err := iptables.RunOutput(ctx, "-t", "mangle", "-S", entwareMSSChain)
	if err != nil {
		return false
	}
	if !strings.Contains(out, "TCPMSS") {
		return false
	}
	for _, cidr := range peerCIDRs {
		if cidr != "" && !strings.Contains(out, cidr) {
			return false
		}
	}
	return true
}

// entwareMSSPresent reports whether TCPMSS clamp for peerCIDR is installed.
func entwareMSSPresent(ctx context.Context, peerCIDR string) bool {
	if peerCIDR == "" {
		return true
	}
	return entwareMSSPresentAll(ctx, []string{peerCIDR})
}

func setupEntwareMSSClamp(ctx context.Context, peerCIDRs ...string) {
	var cidrs []string
	for _, c := range peerCIDRs {
		c = strings.TrimSpace(c)
		if c != "" {
			cidrs = append(cidrs, c)
		}
	}
	if len(cidrs) == 0 {
		return
	}
	_ = iptables.Run(ctx, "-t", "mangle", "-N", entwareMSSChain)
	_ = iptables.Run(ctx, "-t", "mangle", "-F", entwareMSSChain)
	for _, peerCIDR := range cidrs {
		for _, spec := range []string{"-s", "-d"} {
			// Без -m comment намеренно: правила живут в НАШЕЙ цепочке
			// awgm_wdtt_mangle, которую мы же создаём (-N) и сносим (-F/-X).
			// Имя цепочки и есть признак владения — метка не добавила бы
			// ничего, а сверка (entwareMSSPresentAll) читает -S этой цепочки.
			_ = iptables.Run(ctx, "-t", "mangle", "-A", entwareMSSChain,
				spec, peerCIDR, "-p", "tcp", "--tcp-flags", "SYN,RST", "SYN",
				"-j", "TCPMSS", "--clamp-mss-to-pmtu")
		}
	}
	for i := 0; i < 3; i++ {
		_ = iptables.Run(ctx, "-t", "mangle", "-D", "FORWARD", "-j", entwareMSSChain)
	}
	_ = iptables.Run(ctx, "-t", "mangle", "-I", "FORWARD", "1", "-j", entwareMSSChain)
}

func removeEntwareMSSClamp(ctx context.Context) {
	for i := 0; i < 3; i++ {
		_ = iptables.Run(ctx, "-t", "mangle", "-D", "FORWARD", "-j", entwareMSSChain)
	}
	_ = iptables.Run(ctx, "-t", "mangle", "-F", entwareMSSChain)
	_ = iptables.Run(ctx, "-t", "mangle", "-X", entwareMSSChain)
}

// resolveExtIfaceOrDefault возвращает явный wanDev или (если не задан) дефолтный
// WAN по default-маршруту. Общая точка для applyEntwareNATForServer и
// nat-reconcile: без неё internet-only при незаданном/неразрешённом wanDev
// молча деградирует в full-форму MASQUERADE (`! -o`, любой egress) — H1, PR #697.
func resolveExtIfaceOrDefault(ctx context.Context, wanDev string) (string, error) {
	extIface := strings.TrimSpace(wanDev)
	if extIface != "" {
		return extIface, nil
	}
	return defaultWANDev(ctx)
}

func defaultWANDev(ctx context.Context) (string, error) {
	res, err := exec.Run(ctx, "/opt/sbin/ip", "route", "show", "default")
	if err != nil {
		return "", fmt.Errorf("default route: %w", err)
	}
	fields := strings.Fields(strings.TrimSpace(res.Stdout))
	for i, f := range fields {
		if f == "dev" && i+1 < len(fields) {
			return fields[i+1], nil
		}
	}
	return "", fmt.Errorf("default route: no dev in %q", strings.TrimSpace(res.Stdout))
}
