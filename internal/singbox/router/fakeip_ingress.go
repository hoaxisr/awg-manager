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
// Режим policy-tun переиспользует обе половины: маршрутную — для
// ingress-интерфейсов, перехват DNS — для членов политики. Отличий два: отбор
// правил перехвата идёт по connmark политики, а не по `-i`, и правила несут
// свой тег (PolicyTunDNSTag), чтобы режимы не сносили правила друг друга.
//
// Долговечность: в fakeip своего netfilter.d-хука нет (в отличие от tproxy) —
// сброшенные при перезагрузке firewall NDMS правила восстанавливает
// drift-heal в reconcileFakeIPTun, то есть в пределах тика планировщика. У
// перехвата policy-tun хук есть (52-awgm-policytun-dns.sh): режим fail-closed,
// и тика ожидания там быть не должно.

const (
	// FakeIPIngressTag — comment-тег DNAT-правил перехвата DNS. По нему они
	// находятся для идемпотентной переустановки и снятия.
	FakeIPIngressTag = "AWGM-FAKEIP-INGRESS"
	// PolicyTunDNSTag — comment-тег DNAT-правил перехвата DNS в режиме
	// policy-tun. ОБЯЗАН отличаться от FakeIPIngressTag: реап
	// (ReapOrphanedFakeIPTun) в policy-tun каждый тик сносит правила со
	// СВОИМ тегом, и общий тег означал бы churn каждые 30 секунд.
	PolicyTunDNSTag = "AWGM-POLICYTUN-DNS"
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
	// При NoDNAT не используется и может быть пуст.
	TunDNS string
	// NoDNAT — ставить только маршрутную половину заворота (ip rule iif +
	// таблица 700), без перехвата DNS. Перехват сам по себе возможен и в
	// policy-tun (DNS туннеля — тот же адрес), поэтому NoDNAT остаётся только
	// для случая, когда перехват выключен намеренно.
	NoDNAT bool
	// Ifaces — резолвленные kernel-имена ingress-интерфейсов (напр. "opkgtun17").
	Ifaces []string
	// Tag — comment-тег DNAT-правил этого спека. Пусто = FakeIPIngressTag.
	Tag string
	// Marks — connmark политик (hex "0x…"), чей DNS перехватывается.
	// Только policy-tun; в fakeip всегда пусто.
	Marks []string
}

func (spec FakeIPIngressSpec) tag() string {
	if spec.Tag != "" {
		return spec.Tag
	}
	return FakeIPIngressTag
}

// dnatSelectors — селекторы правил перехвата в порядке сборки: сначала
// политики (по connmark), затем ingress-интерфейсы (по -i).
func (spec FakeIPIngressSpec) dnatSelectors() [][]string {
	out := make([][]string, 0, len(spec.Marks)+len(spec.Ifaces))
	for _, m := range spec.Marks {
		out = append(out, []string{"-m", "connmark", "--mark", m})
	}
	for _, i := range spec.Ifaces {
		out = append(out, []string{"-i", i})
	}
	return out
}

// routeHalf — ставится ли маршрутная половина заворота (ip rule iif +
// таблица 700). Она имеет смысл только для ingress-интерфейсов: члены
// политики попадают в tun припаркованным дефолтом NDMS, а не нашей таблицей.
func (spec FakeIPIngressSpec) routeHalf() bool { return len(spec.Ifaces) > 0 }

// dnatHalf — ставится ли перехват DNS.
func (spec FakeIPIngressSpec) dnatHalf() bool {
	return spec.TunIface != "" && !spec.NoDNAT && spec.TunDNS != "" &&
		(len(spec.Marks) > 0 || len(spec.Ifaces) > 0)
}

// active — есть ли что устанавливать вообще. Пустой спек означает
// «заворота быть не должно» и приводит к полному снятию. Клауза
// (NoDNAT || TunDNS != "") сохранена от прежней редакции: спек с
// интерфейсами, но без адреса DNS — это нерезолвленный туннель, и заворот
// маршрутов без перехвата даёт ровно ту поломку, ради которой заворот и делался.
func (spec FakeIPIngressSpec) active() bool {
	return spec.TunIface != "" && (spec.NoDNAT || spec.TunDNS != "") &&
		(spec.routeHalf() || spec.dnatHalf())
}

func fakeIPIngressTableStr() string { return strconv.Itoa(fakeIPIngressTable) }

// fakeIPIngressDNATArgs собирает вставку правила перехвата DNS для одного
// селектора. Вставка в позицию 1: правило обязано стоять выше DNS-редиректов
// NDMS, иначе запрос уйдёт в ndnproxy.
func fakeIPIngressDNATArgs(sel []string, proto, tunDNS, tag string) []string {
	args := make([]string, 0, len(sel)+14)
	args = append(args, "-t", "nat", "-I", "PREROUTING", "1")
	args = append(args, sel...)
	return append(args,
		"-p", proto, "--dport", "53",
		"-m", "comment", "--comment", tag,
		"-j", "DNAT", "--to-destination", net.JoinHostPort(tunDNS, "53"))
}

// lineHasIngressSelector сообщает, что строка дампа несёт заданный селектор.
//
// Для `-i` обязателен разделитель на конце, иначе "opkgtun1" совпал бы с
// "opkgtun17". Для connmark сравнивается ЗНАЧЕНИЕ марки без маски: часть
// сборок iptables печатает `--mark 0x…/0xffffffff`, и буквальное сравнение
// строки давало бы вечный дрейф.
func lineHasIngressSelector(line string, sel []string) bool {
	if len(sel) == 2 && sel[0] == "-i" {
		return strings.Contains(line, "-i "+sel[1]+" ") || strings.HasSuffix(line, "-i "+sel[1])
	}
	i := strings.Index(line, "--mark ")
	if i < 0 {
		return false
	}
	val, _, _ := strings.Cut(line[i+len("--mark "):], " ")
	base, _, _ := strings.Cut(val, "/")
	return strings.EqualFold(base, sel[len(sel)-1])
}

// fakeIPIngressNATDrift сравнивает дамп `iptables -t nat -S PREROUTING` с
// желаемым набором правил перехвата. Дрейфом считается пропажа правила, наш
// тег на правиле вне желаемого набора (например протухшая марка), неверный
// адрес назначения и позиция ниже чужих правил — последнее потому, что
// DNS-редирект NDMS, вставленный позже нас, молча победил бы.
//
// Своими считаются ТОЛЬКО правила с тегом этого спека: тег чужого режима —
// такое же чужое правило, как правило NDMS.
func fakeIPIngressNATDrift(dump string, spec FakeIPIngressSpec) bool {
	if !spec.dnatHalf() {
		// Правил быть не должно: их наличие — дрейф, отсутствие — норма.
		return strings.Contains(dump, spec.tag())
	}
	sels := spec.dnatSelectors()
	want := make(map[string]bool, len(sels)*2)
	for _, sel := range sels {
		for _, proto := range []string{"udp", "tcp"} {
			want[strings.Join(sel, " ")+"/"+proto] = false
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
		if !strings.Contains(line, spec.tag()) {
			leading = false
			continue
		}
		seen++
		if !leading || !strings.Contains(line, dst) {
			return true
		}
		matched := false
		for _, sel := range sels {
			if !lineHasIngressSelector(line, sel) {
				continue
			}
			for _, proto := range []string{"udp", "tcp"} {
				key := strings.Join(sel, " ") + "/" + proto
				if want[key] || !strings.Contains(line, "-p "+proto+" ") {
					continue
				}
				want[key] = true
				matched = true
				break
			}
			if matched {
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
	// Хук: пишем при перехвате policy-tun, снимаем во всех прочих случаях
	// (чужой режим, отключённый перехват, пустой спек). Решение зависит ТОЛЬКО
	// от спека, поэтому стоит выше дампов: сбой чтения `iptables -S` не должен
	// оставлять файл на диске — откат enable снимает хук именно этим вызовом, и
	// после него персиста уже нет, а реап в policy-tun чужой тег не трогает.
	// Запись сравнивает содержимое, так что в установившемся состоянии это одно
	// чтение файла. Она же идёт до проверки дрейфа: набор марок меняется и
	// тогда, когда правила стоят на месте.
	if spec.tag() == PolicyTunDNSTag && spec.dnatHalf() {
		if it.persistPolicyTunDNSHook != nil {
			// Ошибка НАМЕРЕННО не возвращается наверх: §11 обещает, что без
			// файла хука режим работает, а правила чинит drift-heal.
			// Фатальный возврат отменил бы это обещание на прошивке без
			// каталога /opt/etc/ndm/netfilter.d.
			_ = it.persistPolicyTunDNSHook(policyTunDNSHookScript(spec))
		}
	} else if it.cleanupPolicyTunDNSHook != nil {
		// Безусловно: правила мог стереть NDMS, а уцелевший файл вечно
		// возвращал бы DNAT в снесённый туннель.
		it.cleanupPolicyTunDNSHook()
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
		if strings.Contains(natDump, FakeIPIngressTag) ||
			strings.Contains(natDump, PolicyTunDNSTag) ||
			fakeIPIngressAnyOwnedRule(ruleDump) {
			it.RemoveFakeIPIngress(ctx)
		}
		return nil
	}

	routeDrift := false
	if spec.routeHalf() {
		routeDump, rerr := it.runIPOut(ctx, "route", "show", "table", fakeIPIngressTableStr())
		if rerr != nil {
			return fmt.Errorf("dump table %s: %w", fakeIPIngressTableStr(), rerr)
		}
		routeDrift = fakeIPIngressRouteDrift(routeDump, spec)
	}
	if !fakeIPIngressNATDrift(natDump, spec) &&
		!fakeIPIngressRuleDrift(ruleDump, spec) && !routeDrift {
		return nil
	}

	// Дрейф: пересобираем целиком — так одинаково лечатся и пропажа правил
	// (сброс firewall NDMS), и смена состава ingress-интерфейсов.
	//
	// Свой тег снимаем в любом случае: при выключенном перехвате правила
	// обязаны уйти, а не остаться до конца жизни режима. Без этого смешанный
	// спек (марки + интерфейсы) при каждой пересборке вставлял бы НОВЫЙ
	// комплект DNAT поверх старого.
	it.removeFakeIPIngressDNAT(ctx, natDump, spec.tag())
	if spec.dnatHalf() {
		for _, sel := range spec.dnatSelectors() {
			for _, proto := range []string{"udp", "tcp"} {
				if err := it.runIPTables(ctx, fakeIPIngressDNATArgs(sel, proto, spec.TunDNS, spec.tag())...); err != nil {
					return fmt.Errorf("dnat dns %v/%s: %w", sel, proto, err)
				}
			}
		}
	}

	it.drainFakeIPIngressRules(ctx)
	if !spec.routeHalf() {
		// Ingress-интерфейсов нет — таблица 700 не нужна; чистим остатки.
		_ = it.runIP(ctx, "route", "flush", "table", fakeIPIngressTableStr())
		return nil
	}
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
	// Кто снимает правила перехвата, тот снимает и файл, который их
	// восстанавливает: иначе первое же событие nat вернуло бы DNAT.
	if it.cleanupPolicyTunDNSHook != nil {
		it.cleanupPolicyTunDNSHook()
	}
	dump, err := it.runIPTablesOut(ctx, "-t", "nat", "-S", "PREROUTING")
	if err == nil {
		it.removeFakeIPIngressDNAT(ctx, dump, FakeIPIngressTag)
		it.removeFakeIPIngressDNAT(ctx, dump, PolicyTunDNSTag)
	}
	it.drainFakeIPIngressRules(ctx)
	_ = it.runIP(ctx, "route", "flush", "table", fakeIPIngressTableStr())
}

// RemoveFakeIPIngressDNAT снимает перехват DNS заданного тега, не трогая
// маршрутную половину заворота. Нужен режиму policy-tun: свой заворот там
// живёт (ip rule iif + таблица 700), а протухший DNAT прежнего fakeip обязан
// уйти. Идемпотентно.
func (it *IPTables) RemoveFakeIPIngressDNAT(ctx context.Context, tag string) {
	dump, err := it.runIPTablesOut(ctx, "-t", "nat", "-S", "PREROUTING")
	if err != nil || !strings.Contains(dump, tag) {
		return
	}
	it.removeFakeIPIngressDNAT(ctx, dump, tag)
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

// removeFakeIPIngressDNAT удаляет из nat PREROUTING все правила с заданным
// тегом, разбирая уже снятый дамп (свой, а не removeCommentTaggedRulesFromTable, —
// тот ходит в sysexec мимо подменяемых сидов и не покрывается тестами).
func (it *IPTables) removeFakeIPIngressDNAT(ctx context.Context, dump, tag string) {
	for _, line := range strings.Split(dump, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "-A PREROUTING ") || !strings.Contains(line, tag) {
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
func (s *ServiceImpl) fakeIPIngressSpecFor(ctx context.Context, st *storage.OpkgTunState, sr storage.SingboxRouterSettings) FakeIPIngressSpec {
	if st == nil || !st.Provisioned {
		return FakeIPIngressSpec{}
	}
	tunDNS, err := DeriveTunDNS(s.resolveFakeIPParams(sr).TunAddr4)
	if err != nil {
		return FakeIPIngressSpec{}
	}
	return FakeIPIngressSpec{
		TunIface: tunIfaceName(st.Index),
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

// removeFakeIPIngressDNAT — best-effort обёртка над RemoveFakeIPIngressDNAT
// (nil-безопасная, как ensureFakeIPIngress).
func (s *ServiceImpl) removeFakeIPIngressDNAT(ctx context.Context, tag string) {
	if s.deps.IPTables == nil {
		return
	}
	s.deps.IPTables.RemoveFakeIPIngressDNAT(ctx, tag)
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
