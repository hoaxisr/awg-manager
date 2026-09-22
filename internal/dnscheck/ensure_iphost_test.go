package dnscheck

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/sys/netif"
)

type fakeNDMS struct {
	postResp json.RawMessage
	postErr  error

	mu             sync.Mutex
	postedPayloads []any
}

func (f *fakeNDMS) Post(_ context.Context, payload any) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.postedPayloads = append(f.postedPayloads, payload)
	return f.postResp, f.postErr
}

// posts — снимок отправленного. Нужен там, где POST прилетает из таймера сноса.
func (f *fakeNDMS) posts() []any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]any(nil), f.postedPayloads...)
}

// fakeIPHost is a tiny stand-in for query.IPHostStore.
type fakeIPHost struct {
	mu             sync.Mutex
	entries        map[string]string
	invalidations  int
	overrideLookup func(domain string) (string, bool)
}

func (f *fakeIPHost) Lookup(_ context.Context, domain string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.overrideLookup != nil {
		return f.overrideLookup(domain)
	}
	addr, ok := f.entries[domain]
	return addr, ok
}
func (f *fakeIPHost) Invalidate() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invalidations++
}

func TestLookupIPHost_Found(t *testing.T) {
	svc := &Service{ipHost: &fakeIPHost{entries: map[string]string{
		probeDomain: "192.168.1.1",
	}}}
	addr, ok := svc.lookupIPHost(context.Background(), probeDomain)
	if !ok || addr != "192.168.1.1" {
		t.Errorf("got (%q,%v), want (192.168.1.1,true)", addr, ok)
	}
}

func TestLookupIPHost_OtherDomainsPresent(t *testing.T) {
	svc := &Service{ipHost: &fakeIPHost{entries: map[string]string{
		"other.example": "10.0.0.1",
		probeDomain:     "192.168.1.1",
	}}}
	addr, ok := svc.lookupIPHost(context.Background(), probeDomain)
	if !ok || addr != "192.168.1.1" {
		t.Errorf("got (%q,%v), want (192.168.1.1,true)", addr, ok)
	}
}

func TestLookupIPHost_Missing(t *testing.T) {
	svc := &Service{ipHost: &fakeIPHost{entries: map[string]string{}}}
	_, ok := svc.lookupIPHost(context.Background(), probeDomain)
	if ok {
		t.Error("expected not found on empty list")
	}
}

// Regression for NDMS error 1179781 "not found: ip/host/<domain>".
// An earlier version nested the domain as a map key under ip.host,
// which NDMS treats as a path lookup to an existing record — it then
// errors out because we're trying to create. The correct shape keeps
// domain and address as sibling fields under ip.host.
func TestCreateIPHost_PayloadShape(t *testing.T) {
	fake := &fakeNDMS{}
	svc := &Service{ndms: fake, ipHost: &fakeIPHost{}}

	if err := svc.createIPHost(context.Background(), "awgm-dnscheck.test", "192.168.1.1"); err != nil {
		t.Fatalf("createIPHost: %v", err)
	}
	if len(fake.posts()) != 1 {
		t.Fatalf("expected 1 POST, got %d", len(fake.posts()))
	}

	raw, err := json.Marshal(fake.posts()[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"ip":{"host":{"address":"192.168.1.1","domain":"awgm-dnscheck.test"}}}`
	if string(raw) != want {
		t.Fatalf("payload mismatch:\n got: %s\nwant: %s", raw, want)
	}
}

// On a successful write the cache MUST be invalidated so subsequent
// lookupIPHost calls observe the new entry instead of stale data.
func TestCreateIPHost_InvalidatesCacheOnSuccess(t *testing.T) {
	fake := &fakeNDMS{}
	ip := &fakeIPHost{}
	svc := &Service{ndms: fake, ipHost: ip}

	if err := svc.createIPHost(context.Background(), "awgm-dnscheck.test", "192.168.1.1"); err != nil {
		t.Fatalf("createIPHost: %v", err)
	}
	if ip.invalidations != 1 {
		t.Errorf("expected 1 cache invalidation after successful POST, got %d", ip.invalidations)
	}
}

// stubFirstIPv4 подменяет шов над чтением адреса br0 на время теста.
func stubFirstIPv4(t *testing.T, fn func(string) string) {
	t.Helper()
	old := firstIPv4
	firstIPv4 = fn
	t.Cleanup(func() { firstIPv4 = old })
}

// Дефолт шва — netif.FirstIPv4: иначе адрес роутера в записи ip host перестал
// бы браться у интерфейса.
func TestFirstIPv4Default_IsNetifFirstIPv4(t *testing.T) {
	if reflect.ValueOf(firstIPv4).Pointer() != reflect.ValueOf(netif.FirstIPv4).Pointer() {
		t.Fatal("firstIPv4 по умолчанию обязан быть netif.FirstIPv4")
	}
}

// Regression for the router-log spam: when the entry already matches, we
// must NOT issue a create POST — that's what triggered NDMS to log
// 'Core::Configurator: not found: "ip/host/awgm-dnscheck.test"'.
func TestEnsureIPHost_SkipsPostWhenAlreadyCorrect(t *testing.T) {
	stubFirstIPv4(t, func(iface string) string {
		if iface != "br0" {
			t.Fatalf("адрес читали у %q, а запись строится по br0", iface)
		}
		return "192.168.7.1"
	})
	fake := &fakeNDMS{}
	ip := &fakeIPHost{entries: map[string]string{probeDomain: "192.168.7.1"}}
	svc := &Service{ndms: fake, ipHost: ip}

	if err := svc.ensureIPHost(context.Background()); err != nil {
		t.Fatalf("ensureIPHost: %v", err)
	}

	if len(fake.posts()) != 0 {
		t.Fatalf("совпадающая запись — POST'а быть не должно, got %d", len(fake.posts()))
	}
}

// Контроль к предыдущему: расхождение адреса обязано вылиться ровно в один POST.
func TestEnsureIPHost_PostsWhenAddressDiffers(t *testing.T) {
	stubFirstIPv4(t, func(string) string { return "192.168.7.1" })
	fake := &fakeNDMS{}
	ip := &fakeIPHost{entries: map[string]string{probeDomain: "192.168.7.2"}}
	svc := &Service{ndms: fake, ipHost: ip}

	if err := svc.ensureIPHost(context.Background()); err != nil {
		t.Fatalf("ensureIPHost: %v", err)
	}

	if len(fake.posts()) != 1 {
		t.Fatalf("устаревшая запись — ожидали ровно 1 POST, got %d", len(fake.posts()))
	}
}

// ── Снос записи пробы ────────────────────────────────────────────

// stubTeardownDelay укорачивает окно жизни записи на время теста.
func stubTeardownDelay(t *testing.T, d time.Duration) {
	t.Helper()
	old := probeTeardownDelay
	probeTeardownDelay = d
	t.Cleanup(func() { probeTeardownDelay = old })
}

// Форма снятия — БЕЗ адреса: `no ip host <domain>` снимает все адреса домена,
// поэтому смена LAN-адреса роутера между проверками не оставляет сироту.
// Форма с адресом тоже работает, но только для точного совпадения пары.
func TestDeleteIPHost_PayloadShape(t *testing.T) {
	fake := &fakeNDMS{}
	svc := &Service{ndms: fake, ipHost: &fakeIPHost{}}

	if err := svc.deleteIPHost(context.Background(), "awgm-dnscheck.test"); err != nil {
		t.Fatalf("deleteIPHost: %v", err)
	}
	if len(fake.posts()) != 1 {
		t.Fatalf("expected 1 POST, got %d", len(fake.posts()))
	}
	raw, err := json.Marshal(fake.posts()[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"ip":{"host":{"domain":"awgm-dnscheck.test","no":true}}}`
	if string(raw) != want {
		t.Fatalf("payload mismatch:\n got: %s\nwant: %s", raw, want)
	}
}

func TestDeleteIPHost_InvalidatesCacheOnSuccess(t *testing.T) {
	ip := &fakeIPHost{}
	svc := &Service{ndms: &fakeNDMS{}, ipHost: ip}

	if err := svc.deleteIPHost(context.Background(), probeDomain); err != nil {
		t.Fatalf("deleteIPHost: %v", err)
	}
	if ip.invalidations != 1 {
		t.Errorf("expected 1 cache invalidation after successful POST, got %d", ip.invalidations)
	}
}

// Снятие отсутствующей записи NDMS считает ошибкой и пишет её в журнал роутера
// (`E Dns::Manager: no such record`), поэтому POST'а быть не должно вовсе.
func TestRemoveProbeHost_SkipsPostWhenAbsent(t *testing.T) {
	fake := &fakeNDMS{}
	svc := &Service{ndms: fake, ipHost: &fakeIPHost{entries: map[string]string{
		"other.example": "10.0.0.1",
	}}}

	if err := svc.RemoveProbeHost(context.Background()); err != nil {
		t.Fatalf("RemoveProbeHost: %v", err)
	}
	if len(fake.posts()) != 0 {
		t.Fatalf("записи нет — POST'а быть не должно, got %d", len(fake.posts()))
	}
}

// Контроль к предыдущему: существующая запись обязана вылиться ровно в один POST.
func TestRemoveProbeHost_PostsWhenPresent(t *testing.T) {
	fake := &fakeNDMS{}
	svc := &Service{ndms: fake, ipHost: &fakeIPHost{entries: map[string]string{
		probeDomain: "192.168.7.1",
	}}}

	if err := svc.RemoveProbeHost(context.Background()); err != nil {
		t.Fatalf("RemoveProbeHost: %v", err)
	}
	if len(fake.posts()) != 1 {
		t.Fatalf("ожидали ровно 1 POST, got %d", len(fake.posts()))
	}
}

// Полный цикл: Start заводит запись, таймер её снимает. Без этого запись
// переживала бы удаление пакета (#942).
func TestStart_ArmsProbeHostThenTearsItDown(t *testing.T) {
	stubFirstIPv4(t, func(string) string { return "192.168.7.1" })
	stubTeardownDelay(t, 50*time.Millisecond)

	fake := &fakeNDMS{}
	ip := &fakeIPHost{entries: map[string]string{}}
	// Запись появляется в кэше сразу после создания — иначе снос увидит
	// «записи нет» и пропустит POST.
	ip.overrideLookup = func(domain string) (string, bool) {
		if domain != probeDomain {
			return "", false
		}
		for _, p := range fake.posts() {
			if isDeletePayload(p) {
				return "", false
			}
		}
		return "192.168.7.1", len(fake.posts()) > 0
	}
	svc := newTestService(fake, ip)
	stopTeardown(t, svc)

	if _, err := svc.Start(context.Background(), "192.168.7.100"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	posts := fake.posts()
	if len(posts) != 1 || isDeletePayload(posts[0]) {
		t.Fatalf("Start обязан завести запись: %v", posts)
	}

	waitFor(t, time.Second, func() bool {
		p := fake.posts()
		return len(p) == 2 && isDeletePayload(p[1])
	}, "таймер обязан снять запись")
}

// Повторный Start перезаводит таймер: иначе снос от первого запуска срубил бы
// запись посреди второй проверки.
func TestStart_ReArmsTeardownTimer(t *testing.T) {
	stubFirstIPv4(t, func(string) string { return "192.168.7.1" })
	stubTeardownDelay(t, time.Second)

	fake := &fakeNDMS{}
	ip := &fakeIPHost{entries: map[string]string{probeDomain: "192.168.7.1"}}
	svc := newTestService(fake, ip)
	stopTeardown(t, svc)

	if _, err := svc.Start(context.Background(), "192.168.7.100"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(400 * time.Millisecond)
	if _, err := svc.Start(context.Background(), "192.168.7.100"); err != nil {
		t.Fatalf("Start #2: %v", err)
	}
	// Момент, когда таймер ПЕРВОГО запуска уже отработал бы (1.0 с от начала),
	// а таймер второго — ещё нет (1.4 с).
	time.Sleep(800 * time.Millisecond)
	for _, p := range fake.posts() {
		if isDeletePayload(p) {
			t.Fatal("запись снесена посреди второй проверки — таймер не перезавели")
		}
	}
}

// Снос, догнавший лок уже после повторного Start, обязан отработать вхолостую.
// Stop() тут бессилен: он не ждёт колбэк, который уже проснулся. Сцена строится
// детерминированно — второй Start держит лок внутри ensureIPHost, пока таймер
// первого не встанет в очередь за этим же локом.
func TestArmProbeHost_StaleTeardownDoesNotDeleteFreshEntry(t *testing.T) {
	stubFirstIPv4(t, func(string) string { return "192.168.7.1" })
	stubTeardownDelay(t, 200*time.Millisecond)

	fake := &fakeNDMS{}
	ip := &fakeIPHost{entries: map[string]string{probeDomain: "192.168.7.1"}}
	svc := newTestService(fake, ip)
	stopTeardown(t, svc)

	var hold atomic.Bool
	release := make(chan struct{})
	ip.overrideLookup = func(string) (string, bool) {
		if hold.Load() {
			<-release
		}
		return "192.168.7.1", true
	}

	if err := svc.armProbeHost(context.Background()); err != nil {
		t.Fatalf("armProbeHost: %v", err)
	}

	hold.Store(true)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := svc.armProbeHost(context.Background()); err != nil {
			t.Errorf("armProbeHost #2: %v", err)
		}
	}()

	// Второй Start уже под локом и стоит на release; таймер первого (200 мс)
	// за это время выстрелил и ждёт тот же лок.
	time.Sleep(350 * time.Millisecond)
	close(release)
	wg.Wait()

	for _, p := range fake.posts() {
		if isDeletePayload(p) {
			t.Fatal("устаревший снос стёр запись свежей проверки")
		}
	}
}

// Запись заводится ПОСЛЕ серверных проверок: её окно жизни отсчитывается от
// ответа клиенту, а на холодных кэшах проверки стоят секунды.
func TestStart_ArmsProbeHostAfterServerChecks(t *testing.T) {
	stubFirstIPv4(t, func(string) string { return "192.168.7.1" })
	stubTeardownDelay(t, time.Minute)

	fake := &fakeNDMS{}
	svc := newTestService(fake, &fakeIPHost{})
	svc.dnsProxyConfig = assertNoPostsYet{fake: fake, t: t}
	stopTeardown(t, svc)

	if _, err := svc.Start(context.Background(), "192.168.7.100"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(fake.posts()) != 1 {
		t.Fatalf("после Start ожидали ровно 1 POST (заведение), got %d", len(fake.posts()))
	}
}

// assertNoPostsYet — последняя из серверных проверок; к её вызову записи ещё
// быть не должно.
type assertNoPostsYet struct {
	fake *fakeNDMS
	t    *testing.T
}

func (a assertNoPostsYet) HasEncryptedTransport(context.Context) (bool, error) {
	a.t.Helper()
	if len(a.fake.posts()) != 0 {
		a.t.Error("запись пробы заведена до серверных проверок — окно её жизни начнётся раньше, чем клиент получит ответ")
	}
	return true, nil
}

// stopTeardown гасит висящий таймер, чтобы снос не выстрелил уже после теста
// (в пакете goleak) — Service снаружи такого метода не имеет и не должен.
func stopTeardown(t *testing.T, svc *Service) {
	t.Helper()
	t.Cleanup(func() {
		svc.teardownMu.Lock()
		defer svc.teardownMu.Unlock()
		if svc.teardownTimer != nil {
			svc.teardownTimer.Stop()
		}
	})
}

// На ошибке POST кэш трогать нельзя: запись на месте, а сброшенный кэш заставил
// бы следующий Lookup сходить в NDMS за тем же ответом.
func TestDeleteIPHost_KeepsCacheOnFailure(t *testing.T) {
	ip := &fakeIPHost{}
	svc := &Service{ndms: &fakeNDMS{postErr: errors.New("rci down")}, ipHost: ip}

	if err := svc.deleteIPHost(context.Background(), probeDomain); err == nil {
		t.Fatal("ожидали ошибку от POST")
	}
	if ip.invalidations != 0 {
		t.Errorf("на ошибке POST инвалидаций быть не должно, got %d", ip.invalidations)
	}
}

// Отказ заведения записи обязан доехать до результата проверки: иначе проба
// падает и фронт печатает «клиент использует внешний DNS».
func TestStart_ArmFailureSurfacesAsWarning(t *testing.T) {
	stubFirstIPv4(t, func(string) string { return "" }) // br0 без адреса
	svc := newTestService(&fakeNDMS{}, &fakeIPHost{})
	stopTeardown(t, svc)

	resp, err := svc.Start(context.Background(), "192.168.7.100")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	for _, c := range resp.Checks {
		if c.ID != "dns_probe" {
			continue
		}
		if c.Status != "warning" {
			t.Fatalf("dns_probe: got %q, want warning", c.Status)
		}
		return
	}
	t.Fatal("в ответе нет строки dns_probe")
}

// isDeletePayload отличает снятие от заведения по ключу "no".
func isDeletePayload(payload any) bool {
	m, ok := payload.(map[string]interface{})
	if !ok {
		return false
	}
	ip, ok := m["ip"].(map[string]interface{})
	if !ok {
		return false
	}
	host, ok := ip["host"].(map[string]interface{})
	if !ok {
		return false
	}
	_, isNo := host["no"]
	return isNo
}

func waitFor(t *testing.T, limit time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(msg)
}

// newTestService — Service с заглушками зависимостей, которые Start дёргает
// помимо записи пробы.
func newTestService(ndms ndmsClient, ipHost ipHostStore) *Service {
	return &Service{
		ndms:           ndms,
		ipHost:         ipHost,
		hotspot:        stubHotspot{},
		dnsProxyConfig: stubDNSProxyConfig{},
		dns:            stubDNSRoutes{},
		tunnels:        stubTunnels{},
	}
}

type stubHotspot struct{}

func (stubHotspot) List(context.Context) ([]ndms.Device, error) { return nil, nil }

type stubDNSProxyConfig struct{}

func (stubDNSProxyConfig) HasEncryptedTransport(context.Context) (bool, error) { return true, nil }

type stubDNSRoutes struct{}

func (stubDNSRoutes) ListEnabledCount(context.Context) (int, int) { return 1, 1 }

type stubTunnels struct{}

func (stubTunnels) RunningTunnelNames(context.Context) []string { return []string{"wg0"} }
