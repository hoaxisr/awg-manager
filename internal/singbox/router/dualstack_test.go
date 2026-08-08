package router

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Одностековый фейк ядра.
//
// Зачем: раньше «канал» (бинарь) и «стек» (набор таблиц) совпадали — nft-бинарь
// работал через отдельный от xtables стек ядра, и правила, поставленные одним
// каналом, другому были не видны вовсе. Теперь оба канала — легаси и бандл —
// это два РАЗНЫХ БИНАРЯ над ОДНИМ И ТЕМ ЖЕ стеком xtables: таблица awgm — такая
// же таблица ядра, как mangle и nat, и бандл-бинарь видит их все (Task 4). Фейк
// моделирует это буквально: один общий стор состояния (chains/prerouting), и
// два раннера над ним, различающиеся не данными, а тем, что каждый умеет
// РАСПОЗНАТЬ. Легаси-раннер не декодирует таблицу awgm — как настоящий штатный
// iptables без нашего бандла, он на любой запрос по ней отвечает тем же кодом,
// что и на отсутствующую цепочку (Step 2 брифа). Раздельные журналы команд
// (per-view cmds) остаются — тестам важно знать, КАКОЙ канал что вызвал, даже
// когда оба меняют одно и то же состояние.
//
// Модель намеренно грубая: только то, чем пользуется наш код — тела своих
// цепочек, строки PREROUTING по таблицам, restore-блобы и удаление по
// развёрнутой строке правила.

type fakeStack struct {
	mu sync.Mutex
	// chains[table][chain] — тела наших цепочек (строки после "-A <chain> ").
	chains map[string]map[string][]string
	// prerouting[table] — строки вида "-A PREROUTING ...".
	prerouting map[string][]string
	// restores — число применённых блобов (любым каналом).
	restores int
}

func newFakeStack() *fakeStack {
	return &fakeStack{
		chains:     map[string]map[string][]string{},
		prerouting: map[string][]string{},
	}
}

// restore — модель `iptables-restore --noflush`: объявление цепочки создаёт её
// и ФЛАШИТ, если она уже была (так ведёт себя настоящий restore с --noflush),
// поэтому повторный Install не копит тела цепочек.
func (s *fakeStack) restore(input string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.restores++
	table := ""
	for _, line := range strings.Split(input, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "" || line == "COMMIT":
			continue
		case strings.HasPrefix(line, "*"):
			table = strings.TrimPrefix(line, "*")
		case strings.HasPrefix(line, ":"):
			chain := strings.Fields(strings.TrimPrefix(line, ":"))[0]
			if s.chains[table] == nil {
				s.chains[table] = map[string][]string{}
			}
			s.chains[table][chain] = nil
		case strings.HasPrefix(line, "-A ") || strings.HasPrefix(line, "-I "):
			insert := strings.HasPrefix(line, "-I ")
			fields := strings.Fields(line)
			chain := fields[1]
			rest := fields[2:]
			if insert {
				// "-I PREROUTING 1 ..." — позиция нам не важна, важен факт
				// вставки в начало.
				rest = fields[3:]
			}
			body := "-A " + chain + " " + strings.Join(rest, " ")
			if chain == "PREROUTING" {
				if insert {
					s.prerouting[table] = append([]string{body}, s.prerouting[table]...)
				} else {
					s.prerouting[table] = append(s.prerouting[table], body)
				}
				continue
			}
			if s.chains[table] == nil {
				s.chains[table] = map[string][]string{}
			}
			s.chains[table][chain] = append(s.chains[table][chain], body)
		}
	}
}

// dump — модель `iptables -t <table> -S [chain]`.
func (s *fakeStack) dump(table, chain string) string {
	var b strings.Builder
	if chain == "" || chain == "PREROUTING" {
		b.WriteString("-P PREROUTING ACCEPT\n")
	}
	if chain == "" {
		names := make([]string, 0, len(s.chains[table]))
		for name := range s.chains[table] {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			b.WriteString("-N " + name + "\n")
		}
	}
	if chain == "" || chain == "PREROUTING" {
		for _, line := range s.prerouting[table] {
			b.WriteString(line + "\n")
		}
	}
	if chain == "" {
		names := make([]string, 0, len(s.chains[table]))
		for name := range s.chains[table] {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			for _, line := range s.chains[table][name] {
				b.WriteString(line + "\n")
			}
		}
	} else if chain != "PREROUTING" {
		for _, line := range s.chains[table][chain] {
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

func (s *fakeStack) has(table, chain string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.chains[table][chain]
	return ok
}

// mutate applies -F/-X/-D against the shared store; called by both views.
func (s *fakeStack) mutate(table string, rest []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(rest) == 0 {
		return
	}
	switch rest[0] {
	case "-F":
		if _, ok := s.chains[table][rest[1]]; ok {
			s.chains[table][rest[1]] = nil
		}
	case "-X":
		delete(s.chains[table], rest[1])
	case "-D":
		want := "-A " + strings.Join(rest[1:], " ")
		kept := s.prerouting[table][:0:0]
		for _, line := range s.prerouting[table] {
			if line == want {
				continue
			}
			kept = append(kept, line)
		}
		s.prerouting[table] = kept
	}
}

// preroutingCount сообщает, сколько строк PREROUTING таблицы содержат подстроку.
func (s *fakeStack) preroutingCount(table, needle string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, line := range s.prerouting[table] {
		if strings.Contains(line, needle) {
			n++
		}
	}
	return n
}

func (s *fakeStack) hasChain(table, chain string) bool { return s.has(table, chain) }

func (s *fakeStack) resetRestores() {
	s.mu.Lock()
	s.restores = 0
	s.mu.Unlock()
}

// stackView — один из двух раннеров над общим fakeStack: легаси или бандл.
// decodeAwgm=false воспроизводит штатный бинарь, который таблицу awgm вовсе не
// декодирует — см. комментарий типа выше и Step 2 брифа: гейт остатка
// опрашивает awgm-цепочки ТОЛЬКО бандл-раннером ровно по этой причине.
type stackView struct {
	name       string
	stack      *fakeStack
	decodeAwgm bool
	cmds       []string
}

func (v *stackView) log(format string, args ...any) {
	v.cmds = append(v.cmds, fmt.Sprintf(format, args...))
}

func (v *stackView) journal() string {
	return v.name + ":\n  " + strings.Join(v.cmds, "\n  ")
}

func (v *stackView) reset() { v.cmds = nil }

func (v *stackView) restore(_ context.Context, input string) error {
	v.log("restore")
	v.stack.restore(input)
	return nil
}

func (v *stackView) run(_ context.Context, args ...string) error {
	v.log("%s", strings.Join(args, " "))
	table, rest := splitTable(args)
	if !v.decodeAwgm && table == AwgmTable {
		// Штатный бинарь таблицу awgm не знает вовсе — тот же код возврата,
		// что и на отсутствующей цепочке.
		return fmt.Errorf("iptables: %w", errChainAbsent)
	}
	if len(rest) == 0 {
		return nil
	}
	switch rest[0] {
	case "-nL":
		if v.stack.has(table, rest[1]) {
			return nil
		}
		return fmt.Errorf("iptables: No chain/target/match by that name: %w", errChainAbsent)
	case "-F", "-X", "-D":
		v.stack.mutate(table, rest)
	}
	return nil
}

func (v *stackView) runOut(_ context.Context, args ...string) (string, error) {
	v.log("%s", strings.Join(args, " "))
	table, rest := splitTable(args)
	if !v.decodeAwgm && table == AwgmTable {
		// Штатный бинарь молчит о таблице, которой не знает.
		return "", nil
	}
	if len(rest) > 0 && rest[0] == "-S" {
		chain := ""
		if len(rest) > 1 {
			chain = rest[1]
		}
		return v.stack.dump(table, chain), nil
	}
	return "", nil
}

func splitTable(args []string) (table string, rest []string) {
	for i := 0; i < len(args); i++ {
		if args[i] == "-t" && i+1 < len(args) {
			return args[i+1], args[i+2:]
		}
	}
	return "", args
}

// oneStackBackend — awgmLoader поверх бандл-раннера общего стека.
type oneStackBackend struct {
	view      *stackView
	available bool
}

func (b oneStackBackend) Available() (bool, string) {
	if b.available {
		return true, ""
	}
	return false, "бандл awgm не установлен"
}
func (b oneStackBackend) Load(context.Context) error { return nil }
func (b oneStackBackend) RestoreNoflush(ctx context.Context, in string) error {
	return b.view.restore(ctx, in)
}
func (b oneStackBackend) Run(ctx context.Context, args ...string) error {
	return b.view.run(ctx, args...)
}
func (b oneStackBackend) RunOutput(ctx context.Context, args ...string) (string, error) {
	return b.view.runOut(ctx, args...)
}

// newOneStackIPTables собирает *IPTables, у которого legacy-канал ходит через
// legacyView в общий стек, а awgm-канал (после UseAwgm) — через awgmView в тот
// же стек. Активный канал стартует legacy, как и в проде до выбора бэкенда.
func newOneStackIPTables(legacy *stackView) *IPTables {
	return &IPTables{
		restoreNoflush:       legacy.restore,
		runIPTables:          legacy.run,
		runIPTablesOut:       legacy.runOut,
		legacyRestoreNoflush: legacy.restore,
		legacyRun:            legacy.run,
		legacyRunOut:         legacy.runOut,
		runIP:                func(_ context.Context, _ ...string) error { return nil },
		runIPOut:             func(_ context.Context, _ ...string) (string, error) { return ipPresentDump(), nil },
		persistRules:         func(_, _, _ string) error { return nil },
		persistHook:          func(bool) error { return nil },
		persistDNSHook:       func(bool) error { return nil },
		cleanupHook:          func() {},
		cleanupBlackhole:     func() {},
	}
}

// oneStackHarness — движок в режиме tproxy на одностековом ядре с двумя
// раннерами.
type oneStackHarness struct {
	svc    *ServiceImpl
	stack  *fakeStack
	legacy *stackView // штатный канал
	awgm   *stackView // бандл-канал — тот же стек, но декодирует всё
	it     *IPTables
	// ipCmds — команды `ip`. Половина перехвата в policy-routing от канала не
	// зависит, поэтому её журнал общий, а не у стека.
	ipCmds *[]string
}

func newOneStackHarness(t *testing.T, sr storage.SingboxRouterSettings, awgmAvailable bool) *oneStackHarness {
	t.Helper()
	return newOneStackHarnessOn(t, sr, awgmAvailable, newFakeStack())
}

// newOneStackHarnessOn поднимает НОВЫЙ процесс поверх уже существующего
// состояния ядра — так моделируется рестарт демона.
func newOneStackHarnessOn(t *testing.T, sr storage.SingboxRouterSettings, awgmAvailable bool, stack *fakeStack) *oneStackHarness {
	t.Helper()
	stubListeningProbe(t, func() bool { return true })
	stubAwgmListeningProbe(t, func() bool { return true })

	legacy := &stackView{name: "legacy", stack: stack, decodeAwgm: false}
	awgm := &stackView{name: "awgm", stack: stack, decodeAwgm: true}

	it := newOneStackIPTables(legacy)
	var ipCmds []string
	it.runIP = func(_ context.Context, args ...string) error {
		ipCmds = append(ipCmds, strings.Join(args, " "))
		// `rule del` обязан однажды отказать: дренаж крутится, пока команда
		// не вернула ошибку.
		if len(args) >= 2 && args[0] == "rule" && args[1] == "del" {
			return errENOENT
		}
		return nil
	}

	singbox := newTestSingbox(t)
	singbox.isRunningFn = func() (bool, int) { return true, 1234 }

	svc := newTestService(t, Deps{
		Settings:           newTestSettingsStore(t, sr),
		Policies:           &fakeAccessPolicyProvider{},
		IPTables:           it,
		Awgm:               oneStackBackend{view: awgm, available: awgmAvailable},
		Singbox:            singbox,
		WANIPCollector:     &fakeWANIPCollector{},
		NetfilterPreflight: func(context.Context) error { return nil },
	})
	return &oneStackHarness{svc: svc, stack: stack, legacy: legacy, awgm: awgm, it: it, ipCmds: &ipCmds}
}

func oneStackSettings(awgmBackend bool) storage.SingboxRouterSettings {
	return storage.SingboxRouterSettings{
		Enabled:       true,
		RoutingMode:   "tproxy",
		DeviceMode:    "all",
		WANAutoDetect: true,
		AwgmBackend:   awgmBackend,
	}
}

// Прямой страж C1: в awgm-режиме ОБЕ цепочки перехвата (UDP и TCP) живут в
// одной таблице awgm, а опрос установленности спрашивал бы её в mangle/nat —
// вердикт «не установлено» приходил бы на КАЖДОМ тике, и сверка гнала полный
// Enable со снятием и переустановкой перехвата. Окно без перехвата каждые 30
// секунд.
func TestReconcileAwgmModeConvergesWithoutReinstall(t *testing.T) {
	h := newOneStackHarness(t, oneStackSettings(true), true)
	ctx := context.Background()

	if err := h.svc.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile (первый): %v", err)
	}
	if h.svc.backendMode() != BackendAwgm {
		t.Fatalf("предусловие: бэкенд обязан быть awgm, получили %q", h.svc.backendMode())
	}
	if !h.stack.hasChain(AwgmTable, ChainName) || !h.stack.hasChain(AwgmTable, RedirectChain) {
		t.Fatalf("предусловие: перехват обязан стоять в таблице awgm\n%s\n%s", h.legacy.journal(), h.awgm.journal())
	}
	if h.stack.restores == 0 {
		t.Fatalf("предусловие: первый тик обязан поставить правила\n%s", h.awgm.journal())
	}

	h.legacy.reset()
	h.awgm.reset()
	h.stack.resetRestores()
	if err := h.svc.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile (второй): %v", err)
	}
	if h.stack.restores != 0 {
		t.Fatalf("сошедшийся awgm-режим переустанавливает правила на каждом тике (restores=%d)\n%s\n%s",
			h.stack.restores, h.awgm.journal(), h.legacy.journal())
	}
	both := h.awgm.journal() + h.legacy.journal()
	if strings.Contains(both, "-X "+ChainName) {
		t.Fatalf("сошедшийся awgm-режим сносит живой перехват на каждом тике\n%s", both)
	}
}

// Страж C1 со стороны статуса: Active вычисляется из Probe, и та же ошибка
// раскладки держала бы движок «не работает» при работающем перехвате.
func TestStatusActiveInAwgmMode(t *testing.T) {
	h := newOneStackHarness(t, oneStackSettings(true), true)
	ctx := context.Background()
	if err := h.svc.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	installed, jumps, err := h.it.Probe(ctx)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !installed || !jumps {
		t.Fatalf("Probe в awgm-режиме не видит живой перехват: installed=%v jumps=%v\n%s",
			installed, jumps, h.awgm.journal())
	}
}

// Страж C2: рестарт демона в НЕИЗМЕННОМ режиме не имеет права снимать
// работающий перехват. До этой правки первый тик нового процесса безусловно
// звал Uninstall (джампы, цепочки, дренаж ip rule) и ставил правила заново
// только после похода в RCI за меткой политики и сбора WAN-адресов — окно
// fail-open на каждое обновление пакета, а при медленном RCI минутами. Ошибка
// на любом шаге между ними оставляла бы ядро без перехвата вовсе.
func TestDaemonRestartKeepsLiveRulesInPlace(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name          string
		awgmBackend   bool
		awgmAvailable bool
		table         string // где живёт цепочка перехвата в этом режиме
	}{
		{"legacy", false, false, "mangle"},
		{"legacy с доступным бандлом", false, true, "mangle"},
		{"awgm", true, true, AwgmTable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sr := oneStackSettings(tc.awgmBackend)
			first := newOneStackHarness(t, sr, tc.awgmAvailable)
			if err := first.svc.Reconcile(ctx); err != nil {
				t.Fatalf("Reconcile (первый процесс): %v", err)
			}
			if !first.stack.hasChain(tc.table, ChainName) {
				t.Fatalf("предусловие: перехват обязан стоять в %s\n%s", tc.table, first.awgm.journal())
			}

			// Рестарт демона: новый ServiceImpl поверх того же ядра.
			first.legacy.reset()
			first.awgm.reset()
			restarted := newOneStackHarnessOn(t, sr, tc.awgmAvailable, first.stack)
			if err := restarted.svc.Reconcile(ctx); err != nil {
				t.Fatalf("Reconcile (после рестарта): %v", err)
			}

			both := restarted.legacy.journal() + restarted.awgm.journal()
			// Стек один: имя цепочки (ChainName) одинаково в обеих раскладках,
			// разные только таблицы (mangle/nat vs awgm) — routine-снятие
			// ЧУЖОГО канала (adoptAndClean) законно шлёт -X с тем же именем в
			// СВОЕЙ таблице чужой раскладки. Живым остаётся только снятие в
			// таблице tc.table — её и проверяем, а не голое имя цепочки.
			if strings.Contains(both, "-t "+tc.table+" -X "+ChainName) {
				t.Fatalf("рестарт демона снёс живой перехват — окно fail-open на каждое "+
					"обновление пакета\n%s", both)
			}
			if !first.stack.hasChain(tc.table, ChainName) {
				t.Fatalf("после рестарта перехват обязан остаться на месте\n%s", both)
			}
		})
	}
}

// Страж I1: DNS-RESCUE обязан лежать в nat-таблице firewall'а роутера и
// сниматься ТОЛЬКО legacy-каналом (Task 4: он и в awgm-режиме ставится этим же
// каналом — правило обязано попасть в nat ndm, туда бандл-бинарь не пишет).
// Скраб активным каналом (в awgm-режиме это бандл) legacy-content не видит и не
// трогает — каждый Install добавлял бы новый комплект поверх прежнего.
func TestDNSRescueScrubbedByLegacyChannelInAwgmMode(t *testing.T) {
	stack := newFakeStack()
	legacy := &stackView{name: "legacy", stack: stack, decodeAwgm: false}
	awgm := &stackView{name: "awgm", stack: stack, decodeAwgm: true}
	it := newOneStackIPTables(legacy)
	it.UseAwgm(oneStackBackend{view: awgm, available: true})
	ctx := context.Background()

	spec := RestoreInputSpec{
		PolicyMark: "0xffffaaa",
		AwgmMode:   true,
		LANBridges: []LANBridgeDNSRedir{{Bridge: "br0", Port: 41100}},
	}
	for i := 0; i < 3; i++ {
		if err := it.Install(ctx, spec); err != nil {
			t.Fatalf("Install #%d: %v", i, err)
		}
	}
	if n := stack.preroutingCount("nat", DNSRescueTag); n != 2 {
		t.Fatalf("DNS-RESCUE копится в nat: ожидали 2 правила (udp+tcp), получили %d\n%s",
			n, legacy.journal())
	}
	if strings.Contains(awgm.journal(), DNSRescueTag) {
		t.Fatalf("DNS-RESCUE не должен уходить через бандл-канал — правило обязано лечь через legacy: %s", awgm.journal())
	}
}

// В awgm-режиме DNS-RESCUE — НАШ текущий артефакт, а не остаток чужой жизни: он
// обязан лежать в nat-таблице роутера, и туда его кладёт legacy-канал при
// активном awgm. Снятие правил чужого канала обязано пройти мимо него и мимо
// узкого DNS-хука: снятые здесь, они остались бы снятыми до первого успешного
// Install — упади подъём движка на RCI или на ожидании sing-box, и DNS-RESCUE
// пропал бы ровно в том состоянии «движок мёртв», ради которого он существует.
func TestAdoptInAwgmModeKeepsDNSRescue(t *testing.T) {
	tmp := t.TempDir()
	origDNSHook := netfilterDNSHookPath
	netfilterDNSHookPath = filepath.Join(tmp, "51-awgm-dnsrescue.sh")
	t.Cleanup(func() { netfilterDNSHookPath = origDNSHook })
	if err := os.WriteFile(netfilterDNSHookPath, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	stack := newFakeStack()
	// Состояние ядра: наш DNS-RESCUE в nat роутера плюс настоящий legacy-остаток
	// прошлого запуска (mangle/nat), который adopt как раз обязан снести —
	// перехват этого процесса уже переехал в awgm-раскладку.
	rescue := `-A PREROUTING -i br0 -p udp --dport 53 -m comment --comment "` + DNSRescueTag + `" -j REDIRECT --to-ports 41100`
	stack.prerouting["nat"] = []string{rescue}
	stack.chains["nat"] = map[string][]string{RedirectChain: nil}
	stack.chains["mangle"] = map[string][]string{ChainName: nil}

	h := newOneStackHarnessOn(t, oneStackSettings(true), true, stack)
	h.svc.setBackendState(BackendState{Requested: BackendAwgm, Effective: BackendAwgm})

	if err := h.svc.adoptAndClean(context.Background()); err != nil {
		t.Fatalf("adoptAndClean: %v", err)
	}

	if n := stack.preroutingCount("nat", DNSRescueTag); n != 1 {
		t.Fatalf("adopt снял НАШ DNS-RESCUE: правил осталось %d\n%s", n, h.legacy.journal())
	}
	if _, err := os.Stat(netfilterDNSHookPath); err != nil {
		t.Fatalf("adopt снёс узкий DNS-хук awgm-режима — восстановить его до первого успешного "+
			"Install больше некому: %v\n%s", err, h.legacy.journal())
	}
	// Предохранитель от вырожденного теста: чужие (legacy) остатки adopt снять обязан.
	if stack.hasChain("mangle", ChainName) || stack.hasChain("nat", RedirectChain) {
		t.Fatalf("adopt не снял legacy-остатки прошлого запуска\n%s", h.legacy.journal())
	}
}

// Зеркально: при активном legacy чужая раскладка — awgm-таблица, DNS-RESCUE там
// не бывает вовсе, и ограничение ничего не ломает — свои legacy-правила
// DNS-RESCUE стоят нетронутыми, а awgm-остатки сняты.
func TestAdoptInLegacyModeKeepsOwnDNSRescue(t *testing.T) {
	stack := newFakeStack()
	rescue := `-A PREROUTING -i br0 -p udp --dport 53 -m comment --comment "` + DNSRescueTag + `" -j REDIRECT --to-ports 41100`
	stack.prerouting["nat"] = []string{rescue}
	stack.chains[AwgmTable] = map[string][]string{ChainName: nil, RedirectChain: nil}

	h := newOneStackHarnessOn(t, oneStackSettings(false), true, stack)

	if err := h.svc.adoptAndClean(context.Background()); err != nil {
		t.Fatalf("adoptAndClean: %v", err)
	}

	if n := stack.preroutingCount("nat", DNSRescueTag); n != 1 {
		t.Fatalf("adopt чужого (awgm) канала тронул наш legacy DNS-RESCUE: правил %d\n%s",
			n, h.legacy.journal())
	}
	if stack.hasChain(AwgmTable, ChainName) || stack.hasChain(AwgmTable, RedirectChain) {
		t.Fatalf("adopt не снял awgm-остатки прошлого запуска\n%s", h.awgm.journal())
	}
}

// Движок выключен, а своих правил активный канал не видит — единственный путь,
// на котором Uninstall не зовётся вовсе. Половина перехвата в policy-routing
// (`ip rule` по метке + наша таблица маршрутов) от канала не зависит и снятием
// правил канала не убирается: после краха в awgm-режиме с последующим
// выключением галки она осталась бы до перезагрузки роутера.
func TestReconcileDisabledDrainsPolicyRouting(t *testing.T) {
	sr := oneStackSettings(false)
	sr.Enabled = false
	stack := newFakeStack()
	// legacy-раскладка чиста: цепочек нет, значит installedAny=false и ветка
	// Disable (которая дренирует сама) не сработает. Цепочка-леftover — в
	// awgm-таблице, от предыдущего запуска в другом режиме.
	stack.chains[AwgmTable] = map[string][]string{ChainName: nil}
	h := newOneStackHarnessOn(t, sr, true, stack)

	if err := h.svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	joined := strings.Join(*h.ipCmds, "\n")
	if !strings.Contains(joined, "rule del fwmark") {
		t.Fatalf("правило метки не продренировано, команды ip: %v", *h.ipCmds)
	}
	if !strings.Contains(joined, fmt.Sprintf("route flush table %d", RoutingTable)) {
		t.Fatalf("таблица маршрутов не очищена, команды ip: %v", *h.ipCmds)
	}
}

// Тот же выключенный движок, но legacy-остатки ВИДНЫ: ветка Disable снимает
// правила своим Uninstall, и он же дренирует policy-routing. Дублировать
// дренаж после него незачем.
func TestReconcileDisableDrainsPolicyRoutingOnce(t *testing.T) {
	sr := oneStackSettings(false)
	sr.Enabled = false
	stack := newFakeStack()
	stack.chains["mangle"] = map[string][]string{ChainName: nil}
	h := newOneStackHarnessOn(t, sr, true, stack)

	if err := h.svc.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if n := strings.Count(strings.Join(*h.ipCmds, "\n"),
		fmt.Sprintf("route flush table %d", RoutingTable)); n != 1 {
		t.Fatalf("дренаж policy-routing на пути Disable обязан пройти ровно один раз, "+
			"прошёл %d; команды ip: %v", n, *h.ipCmds)
	}
}
