package deviceproxy

// Тесты graceful-деградации выбранного outbound'а (issue #465): когда
// выбранный router-композит (vpn/vpn2) отсутствует в merged-конфиге
// (слот 20-router припаркован — движок выключен), генерация слота 30
// обязана: (а) не эмитить висячие ссылки на теги композитов в члены
// селектора, (б) подставить default-член композита как selector.default
// (намерение пользователя, а не произвольный выживший член), (в) не
// трогать сохранённый SelectedOutbound, чтобы включение движка вернуло
// композит на место.

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
)

// degradationFixture: движок выключен — в merged-конфиге есть прямой
// awg-выход и sing-box туннель, но нет router-композитов vpn/vpn2.
func degradationFixture() (*fakeSingboxOperator, *fakeAWGOutboundsCatalog, *fakeRouterOutboundsCatalog) {
	sb := &fakeSingboxOperator{
		running: true,
		tags:    []string{"proxy-a"},
		availableTags: map[string]bool{
			"direct": true, "block": true, "dns": true,
			"proxy-a": true, "awg-awg10": true,
		},
	}
	awg := &fakeAWGOutboundsCatalog{tags: []AWGTagInfo{{Tag: "awg-awg10", Label: "awg10"}}}
	cat := &fakeRouterOutboundsCatalog{items: []RouterOutboundInfo{
		{Tag: "vpn", Label: "vpn", Detail: "selector · 2", DefaultMember: "awg-awg10", Members: []string{"awg-awg10", "proxy-a"}},
		{Tag: "vpn2", Label: "vpn2", Detail: "selector · 1", DefaultMember: "proxy-a", Members: []string{"proxy-a"}},
	}}
	return sb, awg, cat
}

func newDegradationService(t *testing.T, sb *fakeSingboxOperator, awg *fakeAWGOutboundsCatalog, cat *fakeRouterOutboundsCatalog) *Service {
	t.Helper()
	store := NewStore(filepath.Join(t.TempDir(), "deviceproxy.json"))
	s := NewService(Deps{Store: store, Singbox: sb, AWGOutbounds: awg})
	s.SetRouterOutbounds(cat)
	return s
}

func TestBuildSpec_UnavailableComposite_FallsBackToDefaultMember(t *testing.T) {
	sb, awg, cat := degradationFixture()
	s := newDegradationService(t, sb, awg, cat)

	spec, err := s.buildSpec(context.Background(), "default", Config{Enabled: true, ListenAll: true, Port: 1099, SelectedOutbound: "vpn"})
	if err != nil {
		t.Fatalf("buildSpec: %v", err)
	}
	if spec.SelectedTag != "awg-awg10" {
		t.Fatalf("SelectedTag = %q, want default member awg-awg10", spec.SelectedTag)
	}
	for _, tag := range spec.SBTags {
		if tag == "vpn" || tag == "vpn2" {
			t.Fatalf("unavailable composite tag %q leaked into selector members: %v", tag, spec.SBTags)
		}
	}
}

func TestBuildSpec_AvailableComposite_Unchanged(t *testing.T) {
	sb, awg, cat := degradationFixture()
	sb.availableTags["vpn"] = true // слот 20 активен — vpn в merged-конфиге
	sb.availableTags["vpn2"] = true
	s := newDegradationService(t, sb, awg, cat)

	spec, err := s.buildSpec(context.Background(), "default", Config{Enabled: true, ListenAll: true, Port: 1099, SelectedOutbound: "vpn"})
	if err != nil {
		t.Fatalf("buildSpec: %v", err)
	}
	if spec.SelectedTag != "vpn" {
		t.Fatalf("SelectedTag = %q, want vpn (no degradation while slot enabled)", spec.SelectedTag)
	}
	found := false
	for _, tag := range spec.SBTags {
		if tag == "vpn" {
			found = true
		}
	}
	if !found {
		t.Fatalf("vpn missing from members while available: %v", spec.SBTags)
	}
}

func TestBuildSpec_DefaultMemberUnavailable_FirstAvailableMember(t *testing.T) {
	sb, awg, cat := degradationFixture()
	// default-член указывает на исчезнувший туннель; первый доступный член — proxy-a.
	cat.items[0] = RouterOutboundInfo{Tag: "vpn", DefaultMember: "ghost", Members: []string{"ghost", "proxy-a"}}
	s := newDegradationService(t, sb, awg, cat)

	spec, err := s.buildSpec(context.Background(), "default", Config{Enabled: true, ListenAll: true, Port: 1099, SelectedOutbound: "vpn"})
	if err != nil {
		t.Fatalf("buildSpec: %v", err)
	}
	if spec.SelectedTag != "proxy-a" {
		t.Fatalf("SelectedTag = %q, want first available member proxy-a", spec.SelectedTag)
	}
}

func TestBuildSpec_AllMembersUnavailable_Direct(t *testing.T) {
	sb, awg, cat := degradationFixture()
	cat.items = []RouterOutboundInfo{{Tag: "vpn", DefaultMember: "ghost", Members: []string{"ghost", "ghost2"}}}
	s := newDegradationService(t, sb, awg, cat)

	spec, err := s.buildSpec(context.Background(), "default", Config{Enabled: true, ListenAll: true, Port: 1099, SelectedOutbound: "vpn"})
	if err != nil {
		t.Fatalf("buildSpec: %v", err)
	}
	if spec.SelectedTag != "direct" {
		t.Fatalf("SelectedTag = %q, want direct as last resort", spec.SelectedTag)
	}
}

func TestBuildSpec_NestedCompositeDefault_ResolvedRecursively(t *testing.T) {
	sb, awg, cat := degradationFixture()
	// vpn.default → vpn2 (тоже недоступный композит) → vpn2.default → proxy-a.
	cat.items = []RouterOutboundInfo{
		{Tag: "vpn", DefaultMember: "vpn2", Members: []string{"vpn2", "awg-awg10"}},
		{Tag: "vpn2", DefaultMember: "proxy-a", Members: []string{"proxy-a"}},
	}
	s := newDegradationService(t, sb, awg, cat)

	spec, err := s.buildSpec(context.Background(), "default", Config{Enabled: true, ListenAll: true, Port: 1099, SelectedOutbound: "vpn"})
	if err != nil {
		t.Fatalf("buildSpec: %v", err)
	}
	if spec.SelectedTag != "proxy-a" {
		t.Fatalf("SelectedTag = %q, want proxy-a via nested composite default", spec.SelectedTag)
	}
}

func TestBuildSpec_CompositeCycle_FallsBackToDirect(t *testing.T) {
	sb, awg, cat := degradationFixture()
	cat.items = []RouterOutboundInfo{
		{Tag: "vpn", DefaultMember: "vpn2", Members: []string{"vpn2"}},
		{Tag: "vpn2", DefaultMember: "vpn", Members: []string{"vpn"}},
	}
	s := newDegradationService(t, sb, awg, cat)

	spec, err := s.buildSpec(context.Background(), "default", Config{Enabled: true, ListenAll: true, Port: 1099, SelectedOutbound: "vpn"})
	if err != nil {
		t.Fatalf("buildSpec: %v", err)
	}
	if spec.SelectedTag != "direct" {
		t.Fatalf("SelectedTag = %q, want direct on composite cycle", spec.SelectedTag)
	}
}

func TestBuildSpec_NilAvailabilityOracle_LegacyBehaviour(t *testing.T) {
	sb, awg, cat := degradationFixture()
	sb.availableTags = nil // нет оркестратора → оракул неизвестен
	s := newDegradationService(t, sb, awg, cat)

	spec, err := s.buildSpec(context.Background(), "default", Config{Enabled: true, ListenAll: true, Port: 1099, SelectedOutbound: "vpn"})
	if err != nil {
		t.Fatalf("buildSpec: %v", err)
	}
	if spec.SelectedTag != "vpn" {
		t.Fatalf("SelectedTag = %q, want vpn (legacy path must not degrade)", spec.SelectedTag)
	}
	found := false
	for _, tag := range spec.SBTags {
		if tag == "vpn" {
			found = true
		}
	}
	if !found {
		t.Fatalf("vpn missing from members on legacy path: %v", spec.SBTags)
	}
}

// Недоступный НЕ-router тег (subscription-selector при припаркованном слоте):
// деградации через композит нет (degradationFor → nil), но и висячий default
// эмитить нельзя — prune вычищал бы его на каждом reload, ломая инвариант
// «prune никогда не трогает свежий слот 30». SelectedTag обязан очиститься.
func TestBuildSpec_UnavailableNonRouterTag_NoDefault(t *testing.T) {
	sb, awg, cat := degradationFixture()
	s := newDegradationService(t, sb, awg, cat)
	s.d.SubscriptionOutbounds = &fakeSubscriptionOutboundsCatalog{
		items: []SubscriptionOutboundInfo{{Tag: "sub-sel", Label: "Sub"}},
	}
	// "sub-sel" отсутствует в avail (слот подписок припаркован) и не является
	// router-композитом.

	spec, err := s.buildSpec(context.Background(), "default", Config{Enabled: true, ListenAll: true, Port: 1099, SelectedOutbound: "sub-sel"})
	if err != nil {
		t.Fatalf("buildSpec: %v", err)
	}
	if spec.SelectedTag != "" {
		t.Fatalf("SelectedTag = %q, want empty (no dangling default for unavailable non-router tag)", spec.SelectedTag)
	}
	for _, tag := range spec.SBTags {
		if tag == "sub-sel" {
			t.Fatalf("unavailable subscription tag leaked into members: %v", spec.SBTags)
		}
	}
}

// recAppLogger записывает уровни/сообщения для проверки дедупликации Warn'ов.
//
// Мьютекс нужен не деградационным тестам (они синхронны), а bus-подписчику:
// он пишет из своей горутины, пока тест поллит count (F78).
type recAppLogger struct {
	mu      sync.Mutex
	entries []string
}

func (r *recAppLogger) AppLog(level logging.Level, _, _, action, target, message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, string(level)+":"+action+":"+target+":"+message)
}

func (r *recAppLogger) count(substr string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.entries {
		if strings.Contains(e, substr) {
			n++
		}
	}
	return n
}

func (r *recAppLogger) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.entries...)
}

// Warn деградации не должен повторяться на каждую регенерацию: логируем
// только смену пары selected→fallback и снятие деградации (Info).
func TestBuildSpec_DegradationWarn_Deduplicated(t *testing.T) {
	sb, awg, cat := degradationFixture()
	log := &recAppLogger{}
	store := NewStore(filepath.Join(t.TempDir(), "deviceproxy.json"))
	s := NewService(Deps{Store: store, Singbox: sb, AWGOutbounds: awg, AppLogger: log})
	s.SetRouterOutbounds(cat)

	cfg := Config{Enabled: true, ListenAll: true, Port: 1099, SelectedOutbound: "vpn"}
	for i := 0; i < 3; i++ {
		if _, err := s.buildSpec(context.Background(), "office", cfg); err != nil {
			t.Fatalf("buildSpec #%d: %v", i, err)
		}
	}
	if n := log.count("warn:degraded-outbound"); n != 1 {
		t.Fatalf("degradation warns = %d, want 1 (deduplicated), log: %v", n, log.entries)
	}

	// Слот снова активен — одно Info о восстановлении, и только один раз.
	sb.availableTags["vpn"] = true
	sb.availableTags["vpn2"] = true
	for i := 0; i < 2; i++ {
		if _, err := s.buildSpec(context.Background(), "office", cfg); err != nil {
			t.Fatalf("buildSpec restored #%d: %v", i, err)
		}
	}
	if n := log.count("info:degraded-outbound"); n != 1 {
		t.Fatalf("restore infos = %d, want 1, log: %v", n, log.entries)
	}

	// Повторная деградация после восстановления снова логируется.
	delete(sb.availableTags, "vpn")
	delete(sb.availableTags, "vpn2")
	if _, err := s.buildSpec(context.Background(), "office", cfg); err != nil {
		t.Fatalf("buildSpec re-degraded: %v", err)
	}
	if n := log.count("warn:degraded-outbound"); n != 2 {
		t.Fatalf("degradation warns after re-degrade = %d, want 2, log: %v", n, log.entries)
	}
}

// findInstanceSpec ищет спек по ID (снапшот может содержать и default-инстанс).
func findInstanceSpec(specs []ExternalInstanceSpec, id string) (ExternalInstanceSpec, bool) {
	for _, sp := range specs {
		if sp.ID == id {
			return sp, true
		}
	}
	return ExternalInstanceSpec{}, false
}

// ApplyInstances: спек деградирует, хранилище — нет.
func TestApplyInstances_Degraded_StoreUntouched(t *testing.T) {
	sb, awg, cat := degradationFixture()
	s := newDegradationService(t, sb, awg, cat)
	if err := s.d.Store.SaveInstance(Instance{
		ID: "office", Name: "office", Enabled: true, ListenAll: true, Port: 1099,
		SelectedOutbound: "vpn",
	}); err != nil {
		t.Fatalf("SaveInstance: %v", err)
	}

	if err := s.ApplyInstances(context.Background()); err != nil {
		t.Fatalf("ApplyInstances: %v", err)
	}
	got, ok := findInstanceSpec(sb.lastInstanceSpecs, "office")
	if !ok {
		t.Fatalf("office spec missing in applied specs: %+v", sb.lastInstanceSpecs)
	}
	if got.SelectedTag != "awg-awg10" {
		t.Fatalf("applied SelectedTag = %q, want degraded awg-awg10", got.SelectedTag)
	}
	for _, tag := range got.SBTags {
		if tag == "vpn" || tag == "vpn2" {
			t.Fatalf("composite tag leaked into applied members: %v", got.SBTags)
		}
	}
	// Намерение пользователя сохранено.
	in, ok := s.d.Store.GetInstance("office")
	if !ok || in.SelectedOutbound != "vpn" {
		t.Fatalf("stored SelectedOutbound = %q (ok=%v), want vpn untouched", in.SelectedOutbound, ok)
	}
}

// Включение движка обратно: та же генерация возвращает композит.
func TestApplyInstances_ReenabledRouter_RestoresComposite(t *testing.T) {
	sb, awg, cat := degradationFixture()
	s := newDegradationService(t, sb, awg, cat)
	if err := s.d.Store.SaveInstance(Instance{
		ID: "office", Name: "office", Enabled: true, ListenAll: true, Port: 1099,
		SelectedOutbound: "vpn",
	}); err != nil {
		t.Fatalf("SaveInstance: %v", err)
	}
	if err := s.ApplyInstances(context.Background()); err != nil {
		t.Fatalf("ApplyInstances (degraded): %v", err)
	}

	sb.availableTags["vpn"] = true // слот 20 снова активен
	sb.availableTags["vpn2"] = true
	if err := s.ApplyInstances(context.Background()); err != nil {
		t.Fatalf("ApplyInstances (restored): %v", err)
	}
	got, ok := findInstanceSpec(sb.lastInstanceSpecs, "office")
	if !ok {
		t.Fatalf("office spec missing in applied specs: %+v", sb.lastInstanceSpecs)
	}
	if got.SelectedTag != "vpn" {
		t.Fatalf("applied SelectedTag = %q, want composite vpn restored", got.SelectedTag)
	}
}

func TestGetInstanceRuntimeState_ReportsDegradation(t *testing.T) {
	sb, awg, cat := degradationFixture()
	s := newDegradationService(t, sb, awg, cat)
	if err := s.d.Store.SaveInstance(Instance{
		ID: "office", Name: "office", Enabled: true, ListenAll: true, Port: 1099,
		SelectedOutbound: "vpn",
	}); err != nil {
		t.Fatalf("SaveInstance: %v", err)
	}

	st, err := s.GetInstanceRuntimeState(context.Background(), "office")
	if err != nil {
		t.Fatalf("GetInstanceRuntimeState: %v", err)
	}
	if st.DefaultTag != "vpn" {
		t.Fatalf("DefaultTag = %q, want stored vpn", st.DefaultTag)
	}
	if st.DegradedOutbound != "vpn" || st.FallbackTag != "awg-awg10" {
		t.Fatalf("degradation = (%q, %q), want (vpn, awg-awg10)", st.DegradedOutbound, st.FallbackTag)
	}

	// Слот снова активен — деградация исчезает.
	sb.availableTags["vpn"] = true
	sb.availableTags["vpn2"] = true
	st, err = s.GetInstanceRuntimeState(context.Background(), "office")
	if err != nil {
		t.Fatalf("GetInstanceRuntimeState (restored): %v", err)
	}
	if st.DegradedOutbound != "" || st.FallbackTag != "" {
		t.Fatalf("degradation must clear when slot enabled, got (%q, %q)", st.DegradedOutbound, st.FallbackTag)
	}
}

func TestGetRuntimeState_ReportsDegradation(t *testing.T) {
	sb, awg, cat := degradationFixture()
	store := NewStore(filepath.Join(t.TempDir(), "deviceproxy.json"))
	if err := store.Save(Config{Enabled: true, ListenAll: true, Port: 1099, SelectedOutbound: "vpn"}); err != nil {
		t.Fatalf("store.Save: %v", err)
	}
	s := NewService(Deps{Store: store, Singbox: sb, AWGOutbounds: awg})
	s.SetRouterOutbounds(cat)

	st := s.GetRuntimeState(context.Background())
	if st.DegradedOutbound != "vpn" || st.FallbackTag != "awg-awg10" {
		t.Fatalf("degradation = (%q, %q), want (vpn, awg-awg10)", st.DegradedOutbound, st.FallbackTag)
	}
}

// Reconcile НЕ должен выключать инстанс из-за припаркованного слота:
// композит остаётся в каталоге (файл 20-router.json существует), деградация —
// забота генерации, а не reconcile.
func TestReconcile_ParkedRouterSlot_KeepsInstanceEnabled(t *testing.T) {
	sb, awg, cat := degradationFixture()
	s := newDegradationService(t, sb, awg, cat)
	if err := s.d.Store.SaveInstance(Instance{
		ID: "office", Name: "office", Enabled: true, ListenAll: true, Port: 1099,
		SelectedOutbound: "vpn",
	}); err != nil {
		t.Fatalf("SaveInstance: %v", err)
	}

	if err := s.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	in, ok := s.d.Store.GetInstance("office")
	if !ok || !in.Enabled || in.SelectedOutbound != "vpn" {
		t.Fatalf("instance corrupted by Reconcile: %+v (ok=%v)", in, ok)
	}
}
