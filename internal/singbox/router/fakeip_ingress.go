package router

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Ingress-заворот в режиме fakeip-tun (issue #678).
//
// В tproxy-режиме галка «Маршрутизация через sing-box» у сервера помечает
// ingress-интерфейс policy-меткой, и весь его трафик уходит в sing-box через
// AWGM-TPROXY. В fakeip-tun netfilter не ставится вовсе, поэтому там та же
// галка не делала НИЧЕГО: работали только глобальные статические маршруты по
// dst-CIDR (поэтому «по голым IP» маршрутизация шла, а по доменам — нет: DNS
// клиента уходил мимо fakeip-резолвера и домен превращался в реальный IP).
//
// Здесь тот же смысл галки реализован средствами fakeip-режима:
//
//   - DNAT :53 (udp+tcp) с ingress-интерфейса на DNS туннеля — перехват DNS
//     независимо от того, какой резолвер прописан у клиента (в т.ч. адрес
//     самого роутера: nat PREROUTING отрабатывает до маршрутизации, поэтому
//     запрос не успевает уйти в ndnproxy);
//   - `ip rule iif <iface>` в отдельную таблицу с default в fakeip-tun — весь
//     трафик клиентов решается правилами sing-box, а не глобальными маршрутами.
//
// Осознанные ограничения v1 (задокументированы в UI-подсказке у галки):
//   - ICMP через tun не проксируется, у клиентов не работает ping;
//   - весь трафик клиентов идёт через gvisor — нагрузка на CPU выше;
//   - только IPv4: v6-трафик клиентов идёт как прежде, по глобальным
//     маршрутам пула fakeip (перехвата DNS на v6-резолвер тоже нет);
//   - DNS перехватывается только на 53/udp+tcp — DoH/DoT у клиента уводит
//     резолвинг мимо fakeip, и доменные правила для него не сработают;
//   - iif-правило петли не создаёт: direct-выход sing-box идёт локальным
//     сокетом, а не с iif клиента.
//
// Долговечность: своего netfilter.d-хука здесь нет (в отличие от tproxy) —
// сброшенные при перезагрузке firewall NDMS правила восстанавливает
// drift-heal в reconcileFakeIPTun, то есть в пределах тика планировщика.

const (
	// FakeIPIngressTag — comment-тег DNAT-правил перехвата DNS. По нему они
	// находятся для идемпотентной переустановки и снятия.
	FakeIPIngressTag = "AWGM-FAKEIP-INGRESS"
	// fakeIPIngressTable — таблица маршрутизации с default в fakeip-tun.
	// Выбрана вдали от чужих диапазонов: 100 — таблица fwmark-правила tproxy,
	// 400-599 занимает clientroute, политики NDMS живут в низких номерах.
	fakeIPIngressTable = 700
	// fakeIPIngressPriority — приоритет iif-правил. Ниже tproxy-правила
	// (IPRulePriority=30000) и выше системных main/default (32766/32767);
	// таблица local (0) остаётся впереди, поэтому трафик НА адреса роутера
	// в туннель не заворачивается (DNS до него добирается через DNAT выше).
	fakeIPIngressPriority = 29000
)

// fakeIPIngressThrowCIDRs — приватные диапазоны, которые НЕ заворачиваются в
// tun. Без них таблица с одним default увела бы в sing-box и трафик клиентов
// в LAN роутера, сломав «Доступ в LAN» у тех же серверов. `throw` завершает
// поиск в нашей таблице «как будто маршрута нет», и обработка продолжается
// следующим правилом (main) — то есть ровно как до включения галки. Адрес DNS
// туннеля (172.18.0.2 из 172.16/12) от этого не страдает: в main он остаётся
// connected-маршрутом на сам tun.
var fakeIPIngressThrowCIDRs = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}

// FakeIPIngressSpec — желаемое состояние ingress-заворота.
type FakeIPIngressSpec struct {
	// TunIface — kernel-имя fakeip-tun (напр. "opkgtun0").
	TunIface string
	// TunDNS — адрес DNS туннеля (второй хост /30, напр. "172.18.0.2").
	TunDNS string
	// Ifaces — резолвленные kernel-имена ingress-интерфейсов (напр. "opkgtun17").
	Ifaces []string
}

// active сообщает, есть ли что устанавливать: пустой список интерфейсов или
// незаполненные параметры туннеля означают «заворота нет».
func (spec FakeIPIngressSpec) active() bool {
	return len(spec.Ifaces) > 0 && spec.TunIface != "" && spec.TunDNS != ""
}

func fakeIPIngressTableStr() string { return strconv.Itoa(fakeIPIngressTable) }

// ingressSeamsWired — все четыре сида на месте. Частично собранный IPTables
// встречается в тестах соседних путей (tproxy), и для них заворот — не их
// предмет: молча ничего не делаем вместо nil-паники.
func (it *IPTables) ingressSeamsWired() bool {
	return it.runIPTables != nil && it.runIPTablesOut != nil && it.runIP != nil && it.runIPOut != nil
}

// fakeIPIngressDNATArgs собирает аргументы вставки DNAT-правила перехвата DNS.
// Вставка в позицию 1: правило обязано стоять выше DNS-редиректов NDMS, иначе
// запрос уйдёт в ndnproxy мимо fakeip.
func fakeIPIngressDNATArgs(iface, proto, tunDNS string) []string {
	return []string{
		"-t", "nat", "-I", "PREROUTING", "1",
		"-i", iface, "-p", proto, "--dport", "53",
		"-m", "comment", "--comment", FakeIPIngressTag,
		"-j", "DNAT", "--to-destination", net.JoinHostPort(tunDNS, "53"),
	}
}

// fakeIPIngressNATDrift сравнивает дамп `iptables -t nat -S PREROUTING` с
// желаемым набором DNAT-правил. Дрейфом считается не только пропажа правила,
// но и чужой тег-однофамилец, неверный адрес назначения и позиция ниже чужих
// правил (иначе DNS-редирект NDMS, вставленный позже нас, молча победил бы).
func fakeIPIngressNATDrift(dump string, spec FakeIPIngressSpec) bool {
	want := make(map[string]bool, len(spec.Ifaces)*2)
	for _, iface := range spec.Ifaces {
		for _, proto := range []string{"udp", "tcp"} {
			want[iface+"/"+proto] = false
		}
	}
	dst := "--to-destination " + net.JoinHostPort(spec.TunDNS, "53")

	seen := 0
	leading := true // ещё не встретили ни одного чужого правила
	for _, line := range strings.Split(dump, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "-A PREROUTING ") {
			continue
		}
		if !strings.Contains(line, FakeIPIngressTag) {
			leading = false
			continue
		}
		seen++
		if !leading || !strings.Contains(line, dst) {
			return true
		}
		matched := false
		for key, done := range want {
			if done {
				continue
			}
			iface, proto, _ := strings.Cut(key, "/")
			// Пробел на конце обязателен: без него "-i opkgtun1 " совпал бы
			// с правилом для opkgtun17.
			if strings.Contains(line, "-i "+iface+" ") && strings.Contains(line, "-p "+proto+" ") {
				want[key] = true
				matched = true
				break
			}
		}
		if !matched {
			return true // наш тег на правиле, которого нет в желаемом наборе
		}
	}
	return seen != len(want)
}

// fakeIPIngressOwnedRule опознаёт наши строки в дампе `ip rule show` по
// приоритету, а не по «lookup <номер таблицы>»: при наличии имени таблицы в
// /etc/iproute2/rt_tables iproute2 печатает имя, и матч по номеру видел бы
// вечный дрейф, пересобирая правила каждый тик. Приоритет 29000 — наш по той
// же конвенции, что 30000 у tproxy.
func fakeIPIngressOwnedRule(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), strconv.Itoa(fakeIPIngressPriority)+":")
}

func fakeIPIngressAnyOwnedRule(dump string) bool {
	for _, line := range strings.Split(dump, "\n") {
		if fakeIPIngressOwnedRule(line) {
			return true
		}
	}
	return false
}

// fakeIPIngressRuleDrift сравнивает дамп `ip rule show` с желаемым набором
// iif-правил: лишнее наше правило — такой же дрейф, как пропавшее.
func fakeIPIngressRuleDrift(dump string, spec FakeIPIngressSpec) bool {
	want := make(map[string]bool, len(spec.Ifaces))
	for _, iface := range spec.Ifaces {
		want[iface] = false
	}

	seen := 0
	for _, line := range strings.Split(dump, "\n") {
		if !fakeIPIngressOwnedRule(line) {
			continue
		}
		seen++
		matched := false
		for iface, done := range want {
			if done {
				continue
			}
			if strings.Contains(line, "iif "+iface+" ") {
				want[iface] = true
				matched = true
				break
			}
		}
		if !matched {
			return true
		}
	}
	return seen != len(want)
}

// EnsureFakeIPIngress приводит перехват DNS и заворот маршрутов к spec.
// Идемпотентно: при совпадении фактического состояния с желаемым — ни одной
// мутации, только три чтения (дамп nat, ip rule, таблица маршрутов).
// Пустой spec означает «заворота быть не должно» — тогда всё снимается.
func (it *IPTables) EnsureFakeIPIngress(ctx context.Context, spec FakeIPIngressSpec) error {
	if !it.ingressSeamsWired() {
		return nil
	}
	natDump, err := it.runIPTablesOut(ctx, "-t", "nat", "-S", "PREROUTING")
	if err != nil {
		return fmt.Errorf("dump nat PREROUTING: %w", err)
	}
	ruleDump, err := it.runIPOut(ctx, "rule", "show")
	if err != nil {
		return fmt.Errorf("dump ip rules: %w", err)
	}

	if !spec.active() {
		// Снимаем только если есть что снимать — иначе в tproxy-режиме каждый
		// тик reap'а тратил бы вызовы впустую.
		if strings.Contains(natDump, FakeIPIngressTag) || fakeIPIngressAnyOwnedRule(ruleDump) {
			it.RemoveFakeIPIngress(ctx)
		}
		return nil
	}

	routeDump, err := it.runIPOut(ctx, "route", "show", "table", fakeIPIngressTableStr())
	if err != nil {
		return fmt.Errorf("dump table %s: %w", fakeIPIngressTableStr(), err)
	}

	if !fakeIPIngressNATDrift(natDump, spec) &&
		!fakeIPIngressRuleDrift(ruleDump, spec) &&
		!fakeIPIngressRouteDrift(routeDump, spec) {
		return nil
	}

	// Дрейф: пересобираем целиком — так одинаково лечатся и пропажа правил
	// (сброс firewall NDMS), и смена состава ingress-интерфейсов.
	it.removeFakeIPIngressDNAT(ctx, natDump)
	for _, iface := range spec.Ifaces {
		for _, proto := range []string{"udp", "tcp"} {
			if err := it.runIPTables(ctx, fakeIPIngressDNATArgs(iface, proto, spec.TunDNS)...); err != nil {
				return fmt.Errorf("dnat dns %s/%s: %w", iface, proto, err)
			}
		}
	}

	it.drainFakeIPIngressRules(ctx)
	if err := it.buildFakeIPIngressTable(ctx, spec.TunIface); err != nil {
		return err
	}
	for _, iface := range spec.Ifaces {
		if err := it.runIP(ctx, "rule", "add", "iif", iface,
			"table", fakeIPIngressTableStr(),
			"priority", strconv.Itoa(fakeIPIngressPriority)); err != nil {
			if !strings.Contains(err.Error(), "File exists") {
				return fmt.Errorf("ip rule add iif %s: %w", iface, err)
			}
		}
	}
	return nil
}

// RemoveFakeIPIngress снимает перехват и заворот целиком. Идемпотентно —
// безопасно звать, когда ничего не установлено.
func (it *IPTables) RemoveFakeIPIngress(ctx context.Context) {
	if !it.ingressSeamsWired() {
		return
	}
	dump, err := it.runIPTablesOut(ctx, "-t", "nat", "-S", "PREROUTING")
	if err == nil {
		it.removeFakeIPIngressDNAT(ctx, dump)
	}
	it.drainFakeIPIngressRules(ctx)
	_ = it.runIP(ctx, "route", "flush", "table", fakeIPIngressTableStr())
}

// fakeIPIngressRouteDrift сравнивает дамп `ip route show table <ours>` с
// желаемым содержимым: default в текущий tun (лечит смену его индекса) плюс
// throw-исключения приватных сетей. Лишняя строка — тоже дрейф.
func fakeIPIngressRouteDrift(dump string, spec FakeIPIngressSpec) bool {
	seen := 0
	haveDefault := false
	throws := make(map[string]bool, len(fakeIPIngressThrowCIDRs))
	for _, line := range strings.Split(dump, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		seen++
		switch {
		case strings.HasPrefix(line, "default "):
			haveDefault = strings.Contains(line, "dev "+spec.TunIface+" ") ||
				strings.HasSuffix(line, "dev "+spec.TunIface)
		case strings.HasPrefix(line, "throw "):
			throws[strings.Fields(line)[1]] = true
		}
	}
	if !haveDefault || seen != len(fakeIPIngressThrowCIDRs)+1 {
		return true
	}
	for _, cidr := range fakeIPIngressThrowCIDRs {
		if !throws[cidr] {
			return true
		}
	}
	return false
}

// buildFakeIPIngressTable наполняет таблицу заново: flush (снимает default на
// прежний tun и любой мусор) → throw-исключения → default в tun.
//
// Порядок обязателен: default добавляется ПОСЛЕДНИМ, чтобы сбой на throw'ах
// (напр. ядро без их поддержки) не оставил таблицу с одним default — тогда
// LAN-трафик клиентов ушёл бы в tun. Без default таблица ничего не решает,
// поиск проваливается в main, и поведение остаётся прежним.
func (it *IPTables) buildFakeIPIngressTable(ctx context.Context, tunIface string) error {
	_ = it.runIP(ctx, "route", "flush", "table", fakeIPIngressTableStr())
	for _, cidr := range fakeIPIngressThrowCIDRs {
		if err := it.runIP(ctx, "route", "add", "throw", cidr,
			"table", fakeIPIngressTableStr()); err != nil {
			return fmt.Errorf("ip route add throw %s: %w", cidr, err)
		}
	}
	if err := it.runIP(ctx, "route", "add", "default", "dev", tunIface,
		"table", fakeIPIngressTableStr()); err != nil {
		return fmt.Errorf("ip route add default dev %s: %w", tunIface, err)
	}
	return nil
}

// removeFakeIPIngressDNAT удаляет из nat PREROUTING все правила с нашим тегом,
// разбирая уже снятый дамп (свой, а не removeCommentTaggedRulesFromTable, —
// тот ходит в sysexec мимо подменяемых сидов и не покрывается тестами).
func (it *IPTables) removeFakeIPIngressDNAT(ctx context.Context, dump string) {
	for _, line := range strings.Split(dump, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "-A PREROUTING ") || !strings.Contains(line, FakeIPIngressTag) {
			continue
		}
		del := strings.Replace(line, "-A PREROUTING", "-D PREROUTING", 1)
		args := append([]string{"-t", "nat"}, strings.Fields(strings.ReplaceAll(del, `"`, ""))...)
		_ = it.runIPTables(ctx, args...)
	}
}

// fakeIPIngressSpecFor собирает желаемое состояние из персиста fakeip и
// ingress-ref'ов настроек. Пустой spec (нет персиста, не резолвится DNS или
// список ref'ов пуст) означает «заворота быть не должно».
func (s *ServiceImpl) fakeIPIngressSpecFor(ctx context.Context, st *storage.FakeIPState, sr storage.SingboxRouterSettings) FakeIPIngressSpec {
	if st == nil || !st.Provisioned {
		return FakeIPIngressSpec{}
	}
	tunDNS, err := DeriveTunDNS(resolveFakeIPParams(s.deps.FakeIPTun, sr).TunAddr4)
	if err != nil {
		return FakeIPIngressSpec{}
	}
	return FakeIPIngressSpec{
		TunIface: fakeIPIfaceName(st.Index),
		TunDNS:   tunDNS,
		Ifaces:   s.resolveIngressInterfaces(ctx, sr.IngressInterfaces),
	}
}

// ensureFakeIPIngress — best-effort обёртка над EnsureFakeIPIngress: сбой
// заворота не должен ронять enable/reconcile самого fakeip (без него режим
// работает как раньше — по глобальным маршрутам, только без перехвата DNS).
func (s *ServiceImpl) ensureFakeIPIngress(ctx context.Context, spec FakeIPIngressSpec) {
	if s.deps.IPTables == nil {
		return
	}
	if err := s.deps.IPTables.EnsureFakeIPIngress(ctx, spec); err != nil {
		s.appLog.Warn("fakeip-ingress", spec.TunIface, "ingress-заворот: "+err.Error())
	}
}

// drainFakeIPIngressRules удаляет все ip-правила, указывающие в нашу таблицу,
// до ENOENT (по образцу drainFwmarkRules: `ip rule add` без явного дедупа
// накапливает дубли, один del оставил бы остальные).
func (it *IPTables) drainFakeIPIngressRules(ctx context.Context) {
	for i := 0; i < maxIPRuleDrainPasses; i++ {
		if err := it.runIP(ctx, "rule", "del", "table", fakeIPIngressTableStr()); err != nil {
			break
		}
	}
}
