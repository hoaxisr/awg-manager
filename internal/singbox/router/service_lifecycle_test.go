package router

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Issue #689: tproxy-in на 0.0.0.0 ловит любой UDP на TPROXYPort (включая
// пакеты на WAN-IP роутера) и релеит их сам себе — самоподдерживающаяся
// петля флоу. TPROXY-правила задают --on-ip 127.0.0.1, листенер обязан быть
// там же. redirect-in остаётся на 0.0.0.0 — REDIRECT переписывает dst на
// primary IP интерфейса (96a61c77).
func TestEnsureTProxyInbound_ListenSplit(t *testing.T) {
	t.Run("creates canonical listens", func(t *testing.T) {
		out := ensureTProxyInbound(nil, "", false)
		for _, in := range out {
			switch in.Tag {
			case "tproxy-in":
				if in.Listen != "127.0.0.1" {
					t.Errorf("tproxy-in listen = %q, want 127.0.0.1", in.Listen)
				}
			case "redirect-in":
				if in.Listen != "0.0.0.0" {
					t.Errorf("redirect-in listen = %q, want 0.0.0.0", in.Listen)
				}
			}
		}
	})

	// Upgrade path (issue #689): рестарт демона после обновления НЕ трогает
	// sing-box и не переустанавливает iptables → Reconcile идёт через
	// healTProxyInbound, а не через Enable. Его steady-state guard проверял
	// только UDP-timeout'ы — конфиг с верным таймаутом, но listen 0.0.0.0
	// считался здоровым и дрейф не лечился до ручного передёргивания движка.
	t.Run("heal fixes listen drift with healthy timeouts", func(t *testing.T) {
		svc, dir := newOrchedTestService(t)

		cfg := NewEmptyConfig()
		cfg.Inbounds = ensureTProxyInbound(nil, "", false)
		for i := range cfg.Inbounds {
			if cfg.Inbounds[i].Tag == "tproxy-in" {
				cfg.Inbounds[i].Listen = "0.0.0.0" // как писали версии до фикса
			}
		}
		cfg.EnsureUDPTimeoutRule(DefaultUDPTimeout) // ruleOK в guard'е — true
		if err := SaveConfig(filepath.Join(dir, "20-router.json"), cfg); err != nil {
			t.Fatalf("seed active: %v", err)
		}
		if err := svc.deps.Orch.Bootstrap(); err != nil {
			t.Fatalf("bootstrap: %v", err)
		}

		if err := svc.healTProxyInbound(context.Background(), ""); err != nil {
			t.Fatalf("healTProxyInbound: %v", err)
		}

		healed, err := svc.loadAppliedRouterConfig()
		if err != nil {
			t.Fatalf("reload config: %v", err)
		}
		for _, in := range healed.Inbounds {
			if in.Tag == "tproxy-in" && in.Listen != "127.0.0.1" {
				t.Errorf("listen drift not healed: %q", in.Listen)
			}
		}
	})

	t.Run("heals drifted listens", func(t *testing.T) {
		out := ensureTProxyInbound([]Inbound{
			{Type: "tproxy", Tag: "tproxy-in", Listen: "0.0.0.0", ListenPort: TPROXYPort, Network: "udp"},
			{Type: "redirect", Tag: "redirect-in", Listen: "127.0.0.1", ListenPort: RedirectPort},
		}, "", false)
		for _, in := range out {
			switch in.Tag {
			case "tproxy-in":
				if in.Listen != "127.0.0.1" {
					t.Errorf("tproxy-in not healed: listen = %q", in.Listen)
				}
			case "redirect-in":
				if in.Listen != "0.0.0.0" {
					t.Errorf("redirect-in not healed: listen = %q", in.Listen)
				}
			}
		}
	})
}

// В awgm-режиме TCP-перехват уходит на tproxy-порт (терминальный -j TPROXY в
// mangle), поэтому TCP обязан приниматься тем же inbound'ом. В sing-box
// отсутствующее поле network означает «tcp+udp», так что dual-network — это
// Network: "".
func TestAwgmModeEmitsSingleDualNetworkTproxyInbound(t *testing.T) {
	out := ensureTProxyInbound(nil, "5m", true)

	var tproxy, redirect int
	for _, in := range out {
		switch in.Type {
		case "tproxy":
			tproxy++
			if in.Network != "" {
				t.Fatalf("в awgm-режиме tproxy обслуживает оба протокола: network должен быть пуст, получили %q", in.Network)
			}
			if !in.TCPFastOpen {
				t.Error("inbound принимает TCP — tcp_fast_open должен быть включён")
			}
		case "redirect":
			redirect++
		}
	}
	if tproxy != 1 {
		t.Fatalf("ожидали один tproxy-inbound, получили %d", tproxy)
	}
	if redirect != 0 {
		t.Fatalf("redirect-inbound в awgm-режиме не нужен, получили %d", redirect)
	}
}

func TestAwgmModeNormalizesLegacyConfig(t *testing.T) {
	// Конфиг, переживший legacy-эпоху: tproxy-in с network=udp и живой
	// redirect-in. В awgm-режиме первый обязан стать dual-network, второй —
	// исчезнуть. Иначе TCP-перехват уедет в несуществующий inbound.
	in := []Inbound{
		{Type: "tproxy", Tag: "tproxy-in", Network: "udp", ListenPort: TPROXYPort},
		{Type: "redirect", Tag: "redirect-in", ListenPort: RedirectPort},
	}
	out := ensureTProxyInbound(in, "5m", true)

	seenTProxy := false
	for _, e := range out {
		if e.Tag == "redirect-in" {
			t.Fatal("redirect-in обязан быть удалён в awgm-режиме")
		}
		if e.Tag == "tproxy-in" {
			seenTProxy = true
			if e.Network != "" {
				t.Fatalf("tproxy-in обязан стать dual-network, получили %q", e.Network)
			}
			if !e.TCPFastOpen {
				t.Error("tproxy-in принимает TCP — tcp_fast_open должен быть включён")
			}
		}
	}
	if !seenTProxy {
		t.Fatal("tproxy-in пропал из конфига")
	}
}

func TestLegacyModeKeepsSplit(t *testing.T) {
	out := ensureTProxyInbound(nil, "5m", false)

	var tproxy, redirect int
	for _, in := range out {
		switch in.Type {
		case "tproxy":
			tproxy++
			if in.Network != "udp" {
				t.Fatalf("legacy-режим: tproxy только для UDP, получили %q", in.Network)
			}
			if in.TCPFastOpen {
				t.Error("legacy-режим: tcp_fast_open бессмыслен на UDP-only inbound")
			}
		case "redirect":
			redirect++
		}
	}
	if tproxy != 1 || redirect != 1 {
		t.Fatalf("legacy обязан сохранить сплит, получили tproxy=%d redirect=%d", tproxy, redirect)
	}
}

// Обратный переход: конфиг, побывавший в awgm-режиме (dual-network tproxy-in,
// redirect-in отсутствует), при возврате в legacy обязан снова разъехаться на
// пару — иначе TCP пойдёт через REDIRECT в inbound, которого нет.
func TestLegacyModeRestoresSplitFromAwgmConfig(t *testing.T) {
	in := []Inbound{
		{Type: "tproxy", Tag: "tproxy-in", ListenPort: TPROXYPort, TCPFastOpen: true},
	}
	out := ensureTProxyInbound(in, "5m", false)

	var tproxy, redirect int
	for _, e := range out {
		switch e.Tag {
		case "tproxy-in":
			tproxy++
			if e.Network != "udp" {
				t.Errorf("tproxy-in обязан вернуться к UDP-only, получили %q", e.Network)
			}
			if e.TCPFastOpen {
				t.Error("tcp_fast_open обязан быть снят с UDP-only inbound")
			}
		case "redirect-in":
			redirect++
		}
	}
	if tproxy != 1 || redirect != 1 {
		t.Fatalf("ожидали восстановленный сплит, получили tproxy=%d redirect=%d", tproxy, redirect)
	}
}

// ---------------------------------------------------------------------------
// Fail-closed на время подъёма движка
// ---------------------------------------------------------------------------

// notInstalledFakeIPTables — тот же fakeExec-приёмник, что и newFakeIPTables,
// с двумя отличиями. Первое: проверка наличия цепочек перехвата отвечает «не
// установлено» — у newFakeIPTables успешна ЛЮБАЯ команда, значит IsInstalled
// там всегда true, и ветка «перехвата сейчас нет» была бы недостижима. Второе:
// команда с оборванным контекстом не выполняется, как настоящий exec.
func notInstalledFakeIPTables(fe *fakeExec) *IPTables {
	it := newFakeIPTables(fe)
	run := it.runIPTables
	notInstalled := func(ctx context.Context, args ...string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if slices.Contains(args, "-nL") {
			return errors.New("iptables: No chain/target/match by that name")
		}
		return run(ctx, args...)
	}
	it.runIPTables = notInstalled
	it.legacyRun = notInstalled
	return it
}

// restoreIndexOf — индекс первого iptables-restore, чей блоб содержит marker
// (-1, если такого нет). Блоб заглушки отличим от блоба перехвата по имени
// цепочки.
func restoreIndexOf(fe *fakeExec, marker string) int {
	for i, c := range fe.calls {
		if c.kind == "restore" && strings.Contains(c.stdin, marker) {
			return i
		}
	}
	return -1
}

// blackholeRemoveIndex — индекс `-t mangle -F AWGM-BLACKHOLE`, то есть снятия
// заглушки в АКТИВНОЙ (legacy) раскладке. RemoveBlackhole — единственный, кто
// флашит эту цепочку; таблица в матче нужна, чтобы за снятие не сошла уборка
// чужой (awgm) раскладки, которую делает adopt.
func blackholeRemoveIndex(fe *fakeExec) int {
	for i, c := range fe.calls {
		if c.kind != "iptables" || !slices.Contains(c.args, "-F") || !slices.Contains(c.args, BlackholeChain) {
			continue
		}
		if slices.Contains(c.args, udpChainTable(false)) {
			return i
		}
	}
	return -1
}

// newBootBlackholeService — минимальный подъём tproxy в режиме «все
// устройства»: Orch нет (legacy-ветка со Start), перехвата в ядре нет.
func newBootBlackholeService(t *testing.T, fe *fakeExec) *ServiceImpl {
	t.Helper()
	return newBootBlackholeServiceWith(t, notInstalledFakeIPTables(fe))
}

func newBootBlackholeServiceWith(t *testing.T, ipt *IPTables) *ServiceImpl {
	t.Helper()
	sb := newTestSingbox(t)
	sb.isRunningFn = func() (bool, int) { return true, 1234 }
	return newTestService(t, Deps{
		Settings: newTestSettingsStore(t, storage.SingboxRouterSettings{
			RoutingMode:   "tproxy",
			DeviceMode:    "all",
			WANAutoDetect: true,
		}),
		Policies:           &fakeAccessPolicyProvider{},
		IPTables:           ipt,
		Singbox:            sb,
		WANIPCollector:     &fakeWANIPCollector{ips: []string{"203.0.113.7/32"}},
		NetfilterPreflight: func(context.Context) error { return nil },
	})
}

// Между промоутом слота и установкой правил стоит ожидание готовности sing-box
// (минимум 60 с): перехвата ещё нет, и policy-трафик всё это время уходит в WAN
// мимо движка. Окно обязано держаться fail-closed заглушкой, а она — исчезать
// сразу после того, как встал настоящий перехват.
func TestProvisionInstallsBlackholeBeforeWaitingForSingbox(t *testing.T) {
	fe := &fakeExec{}
	stubListeningProbe(t, func() bool { return true })
	svc := newBootBlackholeService(t, fe)

	if err := svc.Enable(context.Background()); err != nil {
		t.Fatalf("enable: %v", err)
	}

	blackhole := restoreIndexOf(fe, "-A "+BlackholeChain+" -j DROP")
	intercept := restoreIndexOf(fe, ":"+ChainName+" - [0:0]")
	if intercept < 0 {
		t.Fatalf("предусловие: перехват не ставился вовсе, вызовы: %+v", fe.calls)
	}
	if blackhole < 0 {
		t.Fatal("fail-closed заглушка на время подъёма не ставилась")
	}
	if blackhole > intercept {
		t.Fatal("заглушка встала ПОСЛЕ перехвата — окно подъёма осталось fail-open")
	}
	removed := blackholeRemoveIndex(fe)
	if removed < 0 {
		t.Fatal("заглушка осталась в ядре поверх работающего перехвата — весь policy-трафик дропается")
	}
	if removed < intercept {
		t.Fatal("заглушка снята ДО установки перехвата — окно, ради которого она ставилась, снова открыто")
	}
	if svc.blackholeActive {
		t.Error("blackholeActive остался выставленным после снятия заглушки")
	}
}

// Провал ожидания готовности — ровно тот сценарий, ради которого заглушка и
// ставится. Настройки в этот момент ещё держат Enabled=false, а ветка Reconcile
// «выключено» заглушку не видит (HasAnyInstalled смотрит только цепочки
// перехвата) — не снять её здесь значит оставить вечный DROP.
func TestProvisionRemovesBootBlackholeWhenSingboxNeverReady(t *testing.T) {
	fe := &fakeExec{}
	stubListeningProbe(t, func() bool { return false }) // готовность не наступает
	svc := newBootBlackholeService(t, fe)

	// bootWait зажат снизу 60 с (bootWaitWithFloor), поэтому ожидание
	// ограничиваем контекстом: waitForSingbox возвращает ctx.Err().
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	if err := svc.Enable(ctx); err == nil {
		t.Fatal("ожидание обязано было провалиться")
	}
	if restoreIndexOf(fe, "-A "+BlackholeChain+" -j DROP") < 0 {
		t.Fatal("предусловие: заглушка на время подъёма не ставилась")
	}
	if blackholeRemoveIndex(fe) < 0 {
		t.Fatal("провал подъёма оставил вечный DROP: цепочка заглушки не снята")
	}
	if svc.blackholeActive {
		t.Fatal("провал подъёма оставил вечный DROP: в настройках Enabled=false и снять его будет некому")
	}
}

// Обрыв запроса в зазоре между вставшим перехватом и снятием заглушки. ctx у
// Enable — запросный (api/singbox_router.go:85), exec с оборванным контекстом
// не выполняется вовсе, а джамп заглушки добавлен РАНЬШЕ джампа перехвата и
// скрабом внутри Install не трогается. Снятие мёртвым контекстом оставило бы
// заглушку впереди перехвата при сохранённом Enabled=true и сброшенном
// blackholeActive — вечный DROP при зелёном статусе, самолечения нет.
func TestProvisionRemovesBootBlackholeWhenClientDisconnectsAfterInstall(t *testing.T) {
	fe := &fakeExec{}
	stubListeningProbe(t, func() bool { return true })
	ipt := notInstalledFakeIPTables(fe)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Рвём контекст ровно на блобе перехвата: Install через него уже прошёл,
	// снятие заглушки — ещё нет.
	intercepted := false
	ipt.restoreNoflush = func(c context.Context, in string) error {
		err := fe.restoreNoflush(c, in)
		if strings.Contains(in, ":"+ChainName+" - [0:0]") {
			intercepted = true
			cancel()
		}
		return err
	}
	ipt.legacyRestoreNoflush = ipt.restoreNoflush
	svc := newBootBlackholeServiceWith(t, ipt)

	_ = svc.Enable(ctx)

	if !intercepted {
		t.Fatalf("предусловие: перехват не ставился вовсе, вызовы: %+v", fe.calls)
	}
	if restoreIndexOf(fe, "-A "+BlackholeChain+" -j DROP") < 0 {
		t.Fatal("предусловие: заглушка на время подъёма не ставилась")
	}
	if blackholeRemoveIndex(fe) < 0 {
		t.Fatal("обрыв запроса сразу после Install оставил заглушку впереди перехвата: тумблер включён, статус зелёный, policy-трафик дропается")
	}
	if svc.blackholeActive {
		t.Error("blackholeActive остался выставленным после снятия заглушки")
	}
}

// Порядок операций проверяют соседние тесты, этот — СОДЕРЖИМОЕ спеки заглушки.
// В policy-режиме с пустой меткой джамп не эмитится вовсе (цепочка есть,
// входить в неё некому — правка становится молчаливым no-op), а MatchAll вместо
// метки уронил бы весь трафик роутера, а не трафик членов политики.
func TestBootBlackholeSpecCarriesPolicySelectorAndBypass(t *testing.T) {
	const (
		mark    = "0xffffaaa"
		bypass  = "192.0.2.0/24"
		udpPort = "51820"
		tcpPort = "1194"
		wanIP   = "203.0.113.7/32"
	)
	fe := &fakeExec{}
	stubListeningProbe(t, func() bool { return true })
	sb := newTestSingbox(t)
	sb.isRunningFn = func() (bool, int) { return true, 1234 }
	svc := newTestService(t, Deps{
		Settings: newTestSettingsStore(t, storage.SingboxRouterSettings{
			RoutingMode:        "tproxy",
			DeviceMode:         "policy",
			PolicyName:         "Policy0",
			BypassExtraSubnets: bypass,
			BypassExtraPorts:   udpPort + " UDP, " + tcpPort + " TCP",
			SelectiveBypass:    true,
			WANAutoDetect:      true,
		}),
		Policies:           &fakeAccessPolicyProvider{mark: mark},
		IPTables:           notInstalledFakeIPTables(fe),
		Singbox:            sb,
		WANIPCollector:     &fakeWANIPCollector{ips: []string{wanIP}},
		NetfilterPreflight: func(context.Context) error { return nil },
	})

	if err := svc.Enable(context.Background()); err != nil {
		t.Fatalf("enable: %v", err)
	}

	i := restoreIndexOf(fe, "-A "+BlackholeChain+" -j DROP")
	if i < 0 {
		t.Fatalf("предусловие: заглушка на время подъёма не ставилась, вызовы: %+v", fe.calls)
	}
	blob := fe.calls[i].stdin
	jump := "-A PREROUTING -m connmark --mark " + mark + " -m conntrack ! --ctstate INVALID -j " + BlackholeChain
	if !strings.Contains(blob, jump) {
		t.Errorf("в заглушку никто не входит по метке политики — правка стала no-op, окно снова fail-open:\n%s", blob)
	}
	if !strings.Contains(blob, "-A "+BlackholeChain+" -d "+bypass+" -j RETURN") {
		t.Errorf("заглушка дропает подсеть, которую пользователь явно исключил из проксирования:\n%s", blob)
	}
	if !strings.Contains(blob, "-A "+BlackholeChain+" -m set ! --match-set "+selectiveSetName+" dst -j RETURN") {
		t.Errorf("в selective-режиме заглушка дропает весь трафик вместо проксируемого подмножества:\n%s", blob)
	}
	if !strings.Contains(blob, "-A "+BlackholeChain+" -p udp --dport "+udpPort+" -j RETURN") {
		t.Errorf("заглушка дропает UDP-порт, который пользователь явно исключил:\n%s", blob)
	}
	if !strings.Contains(blob, "-A "+BlackholeChain+" -p tcp --dport "+tcpPort+" -j RETURN") {
		t.Errorf("заглушка дропает TCP-порт, который пользователь явно исключил:\n%s", blob)
	}
	if !strings.Contains(blob, "-A "+BlackholeChain+" -d "+wanIP+" -j RETURN") {
		t.Errorf("заглушка дропает трафик на WAN-адрес самого роутера:\n%s", blob)
	}
}

// blackholeFailBackend — awgm-канал, у которого не проходит ТОЛЬКО блоб
// заглушки; блоб перехвата встаёт нормально. `-nL` отвечает отказом, то есть
// перехвата в ядре сейчас нет (табличная проба выбора бэкенда идёт другими
// аргументами и остаётся успешной).
type blackholeFailBackend struct{ stubBackend }

func (b blackholeFailBackend) RestoreNoflush(ctx context.Context, input string) error {
	err := b.stubBackend.RestoreNoflush(ctx, input) // попытка видна в restored
	if strings.Contains(input, BlackholeChain) {
		return errors.New("iptables-restore: bad rule")
	}
	return err
}

func (b blackholeFailBackend) Run(ctx context.Context, args ...string) error {
	if slices.Contains(args, "-nL") {
		return errors.New("iptables: No chain/target/match by that name")
	}
	return b.stubBackend.Run(ctx, args...)
}

// Загрузочная заглушка — best-effort: её отказ логируется, и подъём идёт
// дальше. Основанием для fail-safe демоции он быть не должен — иначе отказ
// заглушки сносит только что вставший РАБОЧИЙ awgm-перехват и поднимает всё
// заново на legacy. Сломанный канал правил через мгновение покажет installRules,
// и вот это основание законное.
func TestBootBlackholeFailureDoesNotDemoteAwgm(t *testing.T) {
	fe := &fakeExec{}
	stubListeningProbe(t, func() bool { return true })
	stubAwgmListeningProbe(t, func() bool { return true })
	sb := newTestSingbox(t)
	sb.isRunningFn = func() (bool, int) { return true, 1234 }
	var restored, calls []string
	svc := newTestService(t, Deps{
		Settings: newTestSettingsStore(t, storage.SingboxRouterSettings{
			RoutingMode:   "tproxy",
			DeviceMode:    "all",
			WANAutoDetect: true,
			AwgmBackend:   true,
		}),
		Policies:           &fakeAccessPolicyProvider{},
		IPTables:           notInstalledFakeIPTables(fe),
		Awgm:               blackholeFailBackend{stubBackend{available: true, restored: &restored, calls: &calls}},
		Singbox:            sb,
		WANIPCollector:     &fakeWANIPCollector{},
		NetfilterPreflight: func(context.Context) error { return nil },
	})

	if err := svc.Enable(context.Background()); err != nil {
		t.Fatalf("enable: %v", err)
	}

	if svc.backendMode() != BackendAwgm {
		t.Fatalf("отказ best-effort заглушки увёл движок на %q, снеся рабочий awgm-перехват", svc.backendMode())
	}
	var blackhole, intercept int
	for _, blob := range restored {
		if strings.Contains(blob, BlackholeChain) {
			blackhole++
		}
		if strings.Contains(blob, ":"+ChainName+" - [0:0]") {
			intercept++
		}
	}
	if blackhole != 1 || intercept != 1 {
		t.Errorf("ожидали по одной попытке заглушки и перехвата, получили blackhole=%d intercept=%d", blackhole, intercept)
	}
	// InstallBlackhole пишет файл правил ДО restore: снять его обязаны и после
	// отказавшей попытки, иначе DEAD-ветка хука позже поднимет заглушку по
	// стухшей спеке, невидимую для blackholeActive.
	if !slices.ContainsFunc(calls, func(c string) bool { return strings.Contains(c, "-F "+BlackholeChain) }) {
		t.Errorf("после отказавшей попытки заглушку никто не снял, вызовы канала: %v", calls)
	}
}

// Смерть демона в окне подъёма (kill, питание, паника) оставляет в настройках
// Enabled=false при живой заглушке в ядре: её файл правил пишется ДО restore,
// adopt в этой ветке снимает blackhole только ЧУЖОЙ раскладки, а DEAD-ветка
// netfilter.d-хука в legacy воскрешает заглушку по уцелевшему файлу. Тик сверки
// при выключенном тумблере обязан её снять — иначе вечный DROP policy-трафика.
func TestReconcileDisabledRemovesLeftoverBlackhole(t *testing.T) {
	fe := &fakeExec{}
	stubListeningProbe(t, func() bool { return false })
	svc := newTestService(t, Deps{
		Settings: newTestSettingsStore(t, storage.SingboxRouterSettings{
			RoutingMode:   "tproxy",
			Enabled:       false,
			WANAutoDetect: true,
		}),
		IPTables: notInstalledFakeIPTables(fe), // перехвата в ядре нет: ветка «выключено»
		Singbox:  newTestSingbox(t),
	})
	svc.blackholeActive = true // прошлый процесс успел поставить заглушку

	if err := svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if blackholeRemoveIndex(fe) < 0 {
		t.Fatalf("тумблер выключен, а заглушка в ядре не снята — вечный DROP policy-трафика, вызовы: %+v", fe.calls)
	}
	if svc.blackholeActive {
		t.Error("blackholeActive остался выставленным после снятия заглушки")
	}
}

// Второе условие правки: заглушку ставим ТОЛЬКО когда перехвата сейчас нет.
// Этот путь достижим drift-heal'ом при живых правилах (запаркованный слот при
// работающем движке), и заглушка поверх рабочего перехвата дала бы обрыв на всё
// время ожидания готовности.
func TestProvisionSkipsBootBlackholeWhenInterceptionLive(t *testing.T) {
	fe := &fakeExec{}
	stubListeningProbe(t, func() bool { return true })
	// newFakeIPTables отвечает успехом на любую команду → IsInstalled=true:
	// ровно состояние «цепочки перехвата в ядре живы».
	svc := newBootBlackholeServiceWith(t, newFakeIPTables(fe))

	if err := svc.Enable(context.Background()); err != nil {
		t.Fatalf("enable: %v", err)
	}

	if restoreIndexOf(fe, ":"+ChainName+" - [0:0]") < 0 {
		t.Fatalf("предусловие: перехват не ставился вовсе, вызовы: %+v", fe.calls)
	}
	if restoreIndexOf(fe, "-A "+BlackholeChain+" -j DROP") >= 0 {
		t.Fatal("заглушка встала поверх живого перехвата — drift-heal обрывает трафик на всё ожидание готовности")
	}
	if svc.blackholeActive {
		t.Error("blackholeActive выставлен, хотя заглушку не ставили")
	}
}

// captureAppLog собирает строки журнала: newTestService минует конструктор
// сервиса, поэтому логгер тесты навешивают вручную.
type captureAppLog struct{ lines []string }

func (c *captureAppLog) AppLog(level logging.Level, _, _, action, _, message string) {
	c.lines = append(c.lines, string(level)+" "+action+" "+message)
}

func (c *captureAppLog) has(sub string) bool {
	return slices.ContainsFunc(c.lines, func(l string) bool { return strings.Contains(l, sub) })
}

// Отсутствие conntrack не мешает перехвату встать, но ломает вытеснение
// потоков, ушедших мимо него. Скрипт вытеснения об этом молчит (выходит
// нулём), поэтому сказать обязан демон — и ровно тогда, когда бинаря нет.
func TestProvisionWarnsWhenConntrackMissing(t *testing.T) {
	provision := func(t *testing.T, conntrack string) *captureAppLog {
		t.Helper()
		stubListeningProbe(t, func() bool { return true })
		it := newFakeIPTables(&fakeExec{})
		it.conntrackPath = conntrack
		svc := newBootBlackholeServiceWith(t, it)
		log := &captureAppLog{}
		svc.appLog = logging.NewScopedLogger(log, logging.GroupRouting, logging.SubSingboxRouter)
		if err := svc.Enable(context.Background()); err != nil {
			t.Fatalf("enable: %v", err)
		}
		return log
	}

	t.Run("бинаря нет — предупреждение в журнале", func(t *testing.T) {
		log := provision(t, filepath.Join(t.TempDir(), "conntrack"))
		if !log.has(conntrackBinPath) {
			t.Fatalf("подъём без conntrack прошёл молча, журнал: %v", log.lines)
		}
		if !log.has(string(logging.LevelWarn)) {
			t.Fatalf("строка обязана быть предупреждением, журнал: %v", log.lines)
		}
	})

	t.Run("бинарь на месте — молчим", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "conntrack")
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		if log := provision(t, path); log.has(conntrackBinPath) {
			t.Fatalf("бинарь на месте, а демон жалуется, журнал: %v", log.lines)
		}
	})
}

// Статус — то, что видит пользователь: без этого поля UI не отличит «утечек
// нет» от «их некому вытеснять». Поле обязано приходить из живой пробы, а не
// быть константой, и обязано переспрашиваться (бинарь доставляют руками).
func TestGetStatusReportsConntrackAvailability(t *testing.T) {
	stubListeningProbe(t, func() bool { return false })
	it := newTestIPTables(&fakeExec{})
	it.conntrackPath = filepath.Join(t.TempDir(), "conntrack")
	svc := newTestService(t, Deps{
		Settings: newTestSettingsStore(t, storage.SingboxRouterSettings{PolicyName: "Policy0"}),
		Policies: &fakeAccessPolicyProvider{mark: "0xffffaaa"},
		IPTables: it,
		Singbox:  newTestSingbox(t),
	})

	st, err := svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.ConntrackAvailable {
		t.Fatal("бинаря нет, а статус говорит «есть»")
	}

	if err := os.WriteFile(it.conntrackPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err = svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if !st.ConntrackAvailable {
		t.Fatal("бинарь на месте, а статус говорит «нет»")
	}
}
