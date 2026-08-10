package router

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/hydraroute"
	"github.com/hoaxisr/awg-manager/internal/singbox/router/bypassset"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

type stubCounts map[string]int

func (s stubCounts) GeoIPTagCounts() map[string]int { return s }

func TestValidateBypassGeoIPTags(t *testing.T) {
	svc := &ServiceImpl{deps: Deps{GeoTagCounts: stubCounts{"ru": 20000, "big": 500000}}}
	cases := []struct {
		name string
		tags []string
		ok   bool
	}{
		{"empty_ok", nil, true},
		{"budget_ok", []string{"ru"}, true},
		{"budget_exceeded", []string{"big"}, false},
		{"unknown_tag_ok_zero_count", []string{"nosuch"}, true},
		{"case_insensitive", []string{"RU"}, true},
		{"case_insensitive_over_budget", []string{"BIG"}, false},
		{"sum_over_budget", []string{"ru", "big"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sr := storage.SingboxRouterSettings{BypassGeoIPTags: c.tags}
			err := svc.validateBypassGeoIPTags(sr)
			if c.ok && err != nil {
				t.Fatalf("want ok, got error: %v", err)
			}
			if !c.ok && err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

// Без счётчика (deps.GeoTagCounts == nil) валидация не должна падать: бюджет
// просто неизвестен, а UpdateSettings обязан оставаться рабочим.
func TestValidateBypassGeoIPTagsNilCounter(t *testing.T) {
	svc := &ServiceImpl{}
	sr := storage.SingboxRouterSettings{BypassGeoIPTags: []string{"big"}}
	if err := svc.validateBypassGeoIPTags(sr); err != nil {
		t.Fatalf("want nil error without counter, got %v", err)
	}
}

// ── Адаптер GeoSource ──────────────────────────────────────────────

// stubExpander — GeoTagExpander с фиксированным ответом.
type stubExpander struct {
	lines []string
	err   error
}

func (s stubExpander) ExpandGeoTag(_, _ string) ([]string, string, error) {
	return s.lines, "", s.err
}
func (s stubExpander) ExpandGeoTagTyped(kind, tag string) ([]string, string, error) {
	return s.ExpandGeoTag(kind, tag)
}

// Контракт Populate: notFound проверяется РАНЬШЕ err, поэтому адаптер обязан
// отдавать sentinel как (nil, true, nil) — без ненулевой ошибки рядом.
func TestGeoSourceAdapter_SentinelIsNotFoundWithoutError(t *testing.T) {
	g := geoSourceAdapter{exp: stubExpander{err: fmt.Errorf("тег: %w", hydraroute.ErrGeoTagNotFound)}}
	lines, notFound, err := g.GeoIPTagLines("nosuch")
	if !notFound || err != nil || lines != nil {
		t.Fatalf("want (nil,true,nil), got (%v,%v,%v)", lines, notFound, err)
	}
}

func TestGeoSourceAdapter_OtherErrorIsError(t *testing.T) {
	boom := errors.New("boom")
	g := geoSourceAdapter{exp: stubExpander{err: boom}}
	lines, notFound, err := g.GeoIPTagLines("ru")
	if notFound || !errors.Is(err, boom) || lines != nil {
		t.Fatalf("want (nil,false,boom), got (%v,%v,%v)", lines, notFound, err)
	}
}

func TestGeoSourceAdapter_Success(t *testing.T) {
	g := geoSourceAdapter{exp: stubExpander{lines: []string{"1.2.3.0/24"}}}
	lines, notFound, err := g.GeoIPTagLines("ru")
	if notFound || err != nil || !slices.Equal(lines, []string{"1.2.3.0/24"}) {
		t.Fatalf("want lines, got (%v,%v,%v)", lines, notFound, err)
	}
}

// ── Триггер наполнения ─────────────────────────────────────────────

// waitBypassOutcome ждёт, пока фоновая горутина запишет итог наполнения.
func waitBypassOutcome(t *testing.T, svc *ServiceImpl) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, last, _, _ := svc.BypassSetStatus(); last != "" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("итог наполнения так и не записан")
}

func TestTriggerBypassSetPopulate_NoOp(t *testing.T) {
	cases := []struct {
		name string
		sr   storage.SingboxRouterSettings
	}{
		{"engine_disabled", storage.SingboxRouterSettings{BypassGeoIPTags: []string{"ru"}}},
		{"no_tags", storage.SingboxRouterSettings{Enabled: true}},
		{"fakeip_mode", storage.SingboxRouterSettings{Enabled: true, RoutingMode: "fakeip-tun", BypassGeoIPTags: []string{"ru"}}},
		{"policy_tun_mode", storage.SingboxRouterSettings{Enabled: true, RoutingMode: statePolicyTun, BypassGeoIPTags: []string{"ru"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bus := &mockBus{}
			svc := &ServiceImpl{deps: Deps{Settings: newTestSettingsStore(t, c.sr), Bus: bus}}
			called := false
			svc.populateBypassSet = func(context.Context, []string) (bypassset.PopulateResult, error) {
				called = true
				return bypassset.PopulateResult{}, nil
			}
			svc.TriggerBypassSetPopulate()
			time.Sleep(20 * time.Millisecond)
			if called {
				t.Fatal("наполнение запущено, хотя триггер обязан быть no-op")
			}
			if bus.HasEvent("bypass-set") {
				t.Fatal("no-op не должен публиковать событие")
			}
		})
	}
}

func TestTriggerBypassSetPopulate_StoresOutcomeAndPublishes(t *testing.T) {
	store := newTestSettingsStore(t, storage.SingboxRouterSettings{
		Enabled: true, BypassGeoIPTags: []string{"ru", "nosuch"},
	})
	bus := &mockBus{}
	svc := &ServiceImpl{deps: Deps{Settings: store, Bus: bus}}
	var gotTags []string
	svc.populateBypassSet = func(_ context.Context, tags []string) (bypassset.PopulateResult, error) {
		gotTags = tags
		return bypassset.PopulateResult{EntryCount: 42, CountOK: true, MissingTags: []string{"nosuch"}}, nil
	}
	svc.TriggerBypassSetPopulate()
	waitBypassOutcome(t, svc)

	if !slices.Equal(gotTags, []string{"ru", "nosuch"}) {
		t.Fatalf("теги переданы неверно: %v", gotTags)
	}
	count, ok, last, lastErr, missing := svc.BypassSetStatus()
	if count != 42 || !ok {
		t.Fatalf("счётчик: got (%d,%v), want (42,true)", count, ok)
	}
	if last == "" || lastErr != "" {
		t.Fatalf("last=%q lastErr=%q", last, lastErr)
	}
	if !slices.Equal(missing, []string{"nosuch"}) {
		t.Fatalf("missingTags = %v", missing)
	}
	if !bus.HasEvent("bypass-set") {
		t.Fatal("событие resource:invalidated (bypass-set) не опубликовано")
	}
}

// Ошибка `ipset save` приходит ПОСЛЕ успешного swap: набор живой, дампа нет.
// Статус обязан отдать исходный текст ошибки, а не «наполнение не удалось».
func TestTriggerBypassSetPopulate_SaveErrorKeepsOriginalText(t *testing.T) {
	store := newTestSettingsStore(t, storage.SingboxRouterSettings{
		Enabled: true, BypassGeoIPTags: []string{"ru"},
	})
	svc := &ServiceImpl{deps: Deps{Settings: store}}
	svc.populateBypassSet = func(context.Context, []string) (bypassset.PopulateResult, error) {
		return bypassset.PopulateResult{}, errors.New("ipset save: no space left on device")
	}
	svc.TriggerBypassSetPopulate()
	waitBypassOutcome(t, svc)

	count, ok, _, lastErr, _ := svc.BypassSetStatus()
	if !strings.Contains(lastErr, "ipset save: no space left on device") {
		t.Fatalf("исходная ошибка потеряна: %q", lastErr)
	}
	// CountOK=false ≠ пустой набор: ноль не должен подаваться как факт.
	if ok || count != 0 {
		t.Fatalf("счётчик при CountOK=false: got (%d,%v), want (0,false)", count, ok)
	}
}

func TestTriggerBypassSetPopulate_SingleFlight(t *testing.T) {
	store := newTestSettingsStore(t, storage.SingboxRouterSettings{
		Enabled: true, BypassGeoIPTags: []string{"ru"},
	})
	svc := &ServiceImpl{deps: Deps{Settings: store}}
	entered := make(chan struct{})
	release := make(chan struct{})
	calls := 0
	svc.populateBypassSet = func(context.Context, []string) (bypassset.PopulateResult, error) {
		calls++
		close(entered)
		<-release
		return bypassset.PopulateResult{CountOK: true}, nil
	}
	svc.TriggerBypassSetPopulate()
	<-entered
	svc.TriggerBypassSetPopulate() // наполнение уже идёт — обязан быть no-op
	close(release)
	waitBypassOutcome(t, svc)
	if calls != 1 {
		t.Fatalf("наполнений: %d, want 1 (single-flight)", calls)
	}
}

// ── Reconcile ──────────────────────────────────────────────────────

// Смена состава тегов — это и переустановка правил (появляется/исчезает
// `--match-set`), и пересборка набора.
func TestReconcileInstalled_BypassGeoTagsChanged_ReinstallsAndPopulates(t *testing.T) {
	sr := storage.SingboxRouterSettings{
		Enabled: true, PolicyName: "Policy0", WANAutoDetect: true,
		BypassGeoIPTags: []string{"ru"},
	}
	var lastRestore string
	ipt := newStubIPTables(func(_ context.Context, input string) error {
		lastRestore = input
		return nil
	})
	svc := &ServiceImpl{
		deps: Deps{
			Settings:           newTestSettingsStore(t, sr),
			Policies:           &fakeAccessPolicyProvider{mark: "0xffffaaa"},
			IPTables:           ipt,
			WANIPCollector:     &fakeWANIPCollector{ips: []string{"203.0.113.207/32"}},
			Singbox:            newReadyTestSingbox(t),
			NetfilterPreflight: func(context.Context) error { return nil },
			XtDscpProbe:        func(context.Context) bool { return true },
		},
		currentMark:         "0xffffaaa",
		currentWANIPs:       []string{"203.0.113.207/32"},
		netfilterStateKnown: true,
	}
	populated := make(chan []string, 1)
	svc.populateBypassSet = func(_ context.Context, tags []string) (bypassset.PopulateResult, error) {
		populated <- tags
		return bypassset.PopulateResult{EntryCount: 7, CountOK: true}, nil
	}

	if err := svc.reconcileInstalled(context.Background(), sr); err != nil {
		t.Fatalf("reconcileInstalled: %v", err)
	}
	if !strings.Contains(lastRestore, "--match-set "+bypassSetName) {
		t.Fatalf("правило набора не установлено:\n%s", lastRestore)
	}
	if !slices.Equal(svc.currentBypassGeoIPTags, []string{"ru"}) {
		t.Fatalf("currentBypassGeoIPTags = %v", svc.currentBypassGeoIPTags)
	}
	select {
	case tags := <-populated:
		if !slices.Equal(tags, []string{"ru"}) {
			t.Fatalf("наполнение получило теги %v", tags)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("наполнение не запущено после установки правил")
	}
}

// Снятие последнего тега убирает правило набора из перехвата.
func TestReconcileInstalled_BypassGeoTagsCleared_DropsSetRule(t *testing.T) {
	sr := storage.SingboxRouterSettings{Enabled: true, PolicyName: "Policy0", WANAutoDetect: true}
	var lastRestore string
	ipt := newStubIPTables(func(_ context.Context, input string) error {
		lastRestore = input
		return nil
	})
	svc := &ServiceImpl{
		deps: Deps{
			Settings:           newTestSettingsStore(t, sr),
			Policies:           &fakeAccessPolicyProvider{mark: "0xffffaaa"},
			IPTables:           ipt,
			WANIPCollector:     &fakeWANIPCollector{ips: []string{"203.0.113.207/32"}},
			Singbox:            newReadyTestSingbox(t),
			NetfilterPreflight: func(context.Context) error { return nil },
			XtDscpProbe:        func(context.Context) bool { return true },
		},
		currentMark:            "0xffffaaa",
		currentWANIPs:          []string{"203.0.113.207/32"},
		currentBypassGeoIPTags: []string{"ru"},
		netfilterStateKnown:    true,
	}
	svc.populateBypassSet = func(context.Context, []string) (bypassset.PopulateResult, error) {
		t.Error("наполнение не должно запускаться при пустом списке тегов")
		return bypassset.PopulateResult{}, nil
	}

	if err := svc.reconcileInstalled(context.Background(), sr); err != nil {
		t.Fatalf("reconcileInstalled: %v", err)
	}
	if strings.Contains(lastRestore, "--match-set "+bypassSetName) {
		t.Fatalf("правило набора осталось после снятия тегов:\n%s", lastRestore)
	}
	if svc.currentBypassGeoIPTags != nil {
		t.Fatalf("currentBypassGeoIPTags = %v, want nil", svc.currentBypassGeoIPTags)
	}
}

// ── Зачистка наследия селектива ────────────────────────────────────

func TestRemoveLegacySelectiveFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "disabled"), 0755); err != nil {
		t.Fatal(err)
	}
	doomed := []string{
		"19-selective-routes.json",
		filepath.Join("disabled", "19-selective-routes.json"),
		"selective-snapshot",
		"selective-snapshot.ndjson",
		"selective-last-rebuild",
	}
	keep := filepath.Join(dir, "20-router.json")
	for _, name := range append(append([]string{}, doomed...), "20-router.json") {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	removeLegacySelectiveFiles(dir)

	for _, name := range doomed {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s не удалён (err=%v)", name, err)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("посторонний файл пострадал: %v", err)
	}
}

func TestCleanupLegacySelective_DropsManagedRules(t *testing.T) {
	sb := newTestSingbox(t)
	cfg := NewEmptyConfig()
	cfg.Route.Rules = []Rule{
		{IPCIDR: []string{"1.2.3.4/32"}, Outbound: "vpn", AwgmManaged: "selective-ip"},
		{Domain: []string{"example.com"}, Outbound: "vpn"},
	}
	if err := SaveConfig(filepath.Join(sb.dir, "20-router.json"), cfg); err != nil {
		t.Fatal(err)
	}
	svc := &ServiceImpl{deps: Deps{Singbox: sb}}

	svc.cleanupLegacySelectiveOnce(context.Background())

	got, err := LoadConfig(filepath.Join(sb.dir, "20-router.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Route.Rules) != 1 {
		t.Fatalf("правил осталось %d, want 1: %+v", len(got.Route.Rules), got.Route.Rules)
	}
	if got.Route.Rules[0].AwgmManaged != "" || len(got.Route.Rules[0].Domain) != 1 {
		t.Fatalf("уцелело не то правило: %+v", got.Route.Rules[0])
	}
}

// Однократность: второй вызов не должен трогать конфиг заново.
func TestCleanupLegacySelective_RunsOnce(t *testing.T) {
	sb := newTestSingbox(t)
	svc := &ServiceImpl{deps: Deps{Singbox: sb}}
	svc.cleanupLegacySelectiveOnce(context.Background())

	// Появившееся ПОСЛЕ первой зачистки managed-правило остаётся на месте:
	// зачистка одноразовая, а не периодический фильтр.
	cfg := NewEmptyConfig()
	cfg.Route.Rules = []Rule{{IPCIDR: []string{"1.2.3.4/32"}, AwgmManaged: "selective-ip"}}
	if err := SaveConfig(filepath.Join(sb.dir, "20-router.json"), cfg); err != nil {
		t.Fatal(err)
	}
	svc.cleanupLegacySelectiveOnce(context.Background())
	got, err := LoadConfig(filepath.Join(sb.dir, "20-router.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Route.Rules) != 1 {
		t.Fatalf("зачистка отработала повторно: %+v", got.Route.Rules)
	}
}
