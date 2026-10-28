package pingcheck

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/sys/httpclient"

	"github.com/hoaxisr/awg-manager/internal/httpprobe"
	"github.com/hoaxisr/awg-manager/internal/ndms"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// fakePolicies — политики NDMS в памяти. Deny убирает интерфейс из списка, как
// rc-выдача после `no permit` (запись уезжает в хвост с no:true, а query её
// пропускает); Permit вставляет на позицию order — так NDMS вёл себя на
// стенде 27.10.
type fakePolicies struct {
	lists     map[string][]string
	permitErr map[string]error // по политике
	denyErr   error
	onDeny    func(policy string)
	denies    int
}

func (f *fakePolicies) FreshPolicies(context.Context) ([]ndms.Policy, error) {
	var out []ndms.Policy
	for name, ifaces := range f.lists {
		p := ndms.Policy{Name: name}
		for i, n := range ifaces {
			p.Interfaces = append(p.Interfaces, ndms.PermittedIface{Name: n, Order: i})
		}
		out = append(out, p)
	}
	return out, nil
}

func (f *fakePolicies) DenyInterface(_ context.Context, name, iface string) error {
	f.denies++
	if f.onDeny != nil {
		f.onDeny(name)
	}
	if f.denyErr != nil {
		return f.denyErr
	}
	var kept []string
	for _, n := range f.lists[name] {
		if n != iface {
			kept = append(kept, n)
		}
	}
	f.lists[name] = kept
	return nil
}

func (f *fakePolicies) PermitInterface(_ context.Context, name, iface string, order int) error {
	if err := f.permitErr[name]; err != nil {
		return err
	}
	l := f.lists[name]
	l = append(l[:order], append([]string{iface}, l[order:]...)...)
	f.lists[name] = l
	return nil
}

func holdsOf(t *testing.T, s *Service) []storage.PolicyHold {
	t.Helper()
	stored, err := s.tunnels.Get("awg7")
	if err != nil {
		t.Fatal(err)
	}
	return stored.PolicyHolds
}

// Порог → туннель уходит из политики, следующий интерфейс становится первым;
// дальнейшие провалы позиции не перезаписывают; первая успешная проверка
// возвращает его на прежнее место (F743).
func TestPolicyHold_ReleasedAtThresholdRestoredOnSuccess(t *testing.T) {
	orig := httpprobe.Client
	t.Cleanup(func() { httpprobe.Client = orig })
	s, m, config, _ := newKernelSensorService(t)
	fp := &fakePolicies{lists: map[string][]string{
		"HydraRoute": {"OpkgTun7", "OpkgTun8", "Proxy1"},
		"Policy0":    {"PPPoE0"},
	}}
	s.SetPolicies(fp)

	httpprobe.Client = alwaysFailDoer{}
	for range 3 {
		s.sensorTick(m, config)
	}
	if got := fp.lists["HydraRoute"]; !reflect.DeepEqual(got, []string{"OpkgTun8", "Proxy1"}) {
		t.Fatalf("после порога политика = %v, туннель должен уйти", got)
	}
	if got := holdsOf(t, s); !reflect.DeepEqual(got, []storage.PolicyHold{{Policy: "HydraRoute", Order: 0}}) {
		t.Fatalf("holds = %+v", got)
	}

	for range 3 {
		s.sensorTick(m, config)
	}
	if fp.denies != 1 {
		t.Fatalf("повторное снятие на втором пороге: denies = %d", fp.denies)
	}
	if got := holdsOf(t, s); !reflect.DeepEqual(got, []storage.PolicyHold{{Policy: "HydraRoute", Order: 0}}) {
		t.Fatalf("второй порог перезаписал позицию: %+v", got)
	}

	httpprobe.Client = okDoer{}
	s.sensorTick(m, config)
	if got := fp.lists["HydraRoute"]; !reflect.DeepEqual(got, []string{"OpkgTun7", "OpkgTun8", "Proxy1"}) {
		t.Fatalf("после успеха политика = %v, туннель должен вернуться первым", got)
	}
	if got := holdsOf(t, s); len(got) != 0 {
		t.Fatalf("holds после возврата = %+v", got)
	}
}

// Линк опущен (Suspend на падении WAN) — проверок нет: порог не набирается,
// permit не снимается, во флеш ничего не пишется. Политику с такого
// интерфейса NDMS уводит сам (F750).
func TestPolicyHold_SuspendedLinkNotChecked(t *testing.T) {
	orig := httpprobe.Client
	t.Cleanup(func() { httpprobe.Client = orig })
	s, m, config, _ := newKernelSensorService(t)
	fp := &fakePolicies{lists: map[string][]string{"HydraRoute": {"OpkgTun7", "PPPoE0"}}}
	s.SetPolicies(fp)
	httpprobe.Client = alwaysFailDoer{}

	stubIfaceUp(t, false)
	for range 5 {
		s.sensorTick(m, config)
	}
	if fp.denies != 0 || m.failCount != 0 || repairs(s) != 0 {
		t.Fatalf("опущенный линк проверялся: denies=%d failCount=%d repairs=%d", fp.denies, m.failCount, repairs(s))
	}
	if len(s.logBuffer.GetAll()) != 0 {
		t.Fatal("в журнал попали проверки опущенного линка")
	}

	// Линк поднят (Resume) — мониторинг работает как прежде.
	stubIfaceUp(t, true)
	for range 3 {
		s.sensorTick(m, config)
	}
	if fp.denies != 1 {
		t.Fatalf("после подъёма линка порог не сработал: denies=%d", fp.denies)
	}
}

// downDuringProbeDoer опускает линк, пока проба летит, и проваливает её.
type downDuringProbeDoer struct{ up *bool }

func (d downDuringProbeDoer) Do(context.Context, httpclient.CallConfig) (*httpclient.Result, error) {
	*d.up = false
	return nil, errors.New("network is down")
}

// Линк опустили посреди пробы (Suspend во время проверки): её провал — след
// опускания, порог не набирается и permit не снимается (ревью F750).
func TestPolicyHold_LinkDownDuringProbeNotCounted(t *testing.T) {
	orig := httpprobe.Client
	t.Cleanup(func() { httpprobe.Client = orig })
	s, m, config, _ := newKernelSensorService(t)
	fp := &fakePolicies{lists: map[string][]string{"HydraRoute": {"OpkgTun7", "PPPoE0"}}}
	s.SetPolicies(fp)

	up := true
	old := ifaceUp
	ifaceUp = func(string) bool { return up }
	t.Cleanup(func() { ifaceUp = old })
	httpprobe.Client = downDuringProbeDoer{up: &up}

	for range 3 {
		up = true // линк поднят к началу тика, опускается внутри пробы
		s.sensorTick(m, config)
	}
	if fp.denies != 0 || m.failCount != 0 || len(s.logBuffer.GetAll()) != 0 {
		t.Fatalf("провал пробы на опущенном линке засчитан: denies=%d failCount=%d журнал=%d",
			fp.denies, m.failCount, len(s.logBuffer.GetAll()))
	}
}

// Возврат: удалённая политика и вернувшийся руками интерфейс пропускаются,
// позиция за концом укороченного списка прижимается к концу, неудачный
// возврат остаётся в записи до следующей попытки.
func TestPolicyHold_RestoreEdgeCases(t *testing.T) {
	s, _, _, _ := newKernelSensorService(t)
	fp := &fakePolicies{lists: map[string][]string{
		"Short":  {"Proxy1"},
		"Manual": {"OpkgTun7", "Proxy1"},
	}}
	s.SetPolicies(fp)
	if err := s.tunnels.Update("awg7", func(t *storage.AWGTunnel) error {
		t.PolicyHolds = []storage.PolicyHold{{Policy: "Short", Order: 3}, {Policy: "Manual", Order: 1}, {Policy: "Gone", Order: 0}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	s.restorePolicies("awg7")
	if got := fp.lists["Short"]; !reflect.DeepEqual(got, []string{"Proxy1", "OpkgTun7"}) {
		t.Fatalf("Short = %v", got)
	}
	if got := fp.lists["Manual"]; !reflect.DeepEqual(got, []string{"OpkgTun7", "Proxy1"}) {
		t.Fatalf("Manual продублирован: %v", got)
	}
	if got := holdsOf(t, s); len(got) != 0 {
		t.Fatalf("holds = %+v", got)
	}

	fp.lists["Short"] = []string{"Proxy1"}
	fp.permitErr = map[string]error{"Short": errors.New("rci down")}
	if err := s.tunnels.Update("awg7", func(t *storage.AWGTunnel) error {
		t.PolicyHolds = []storage.PolicyHold{{Policy: "Short", Order: 0}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s.restorePolicies("awg7")
	if got := holdsOf(t, s); !reflect.DeepEqual(got, []storage.PolicyHold{{Policy: "Short", Order: 0}}) {
		t.Fatalf("неудачный возврат потерян: %+v", got)
	}
}

// Выключение мониторинга возвращает permit и тогда, когда монитора нет
// (демон упал с permit'ом снятым).
func TestPolicyHold_StopMonitoringRestoresWithoutMonitor(t *testing.T) {
	s, _, _, _ := newKernelSensorService(t)
	s.monitors = map[string]*tunnelMonitor{}
	fp := &fakePolicies{lists: map[string][]string{"HydraRoute": {"Proxy1"}}}
	s.SetPolicies(fp)
	if err := s.tunnels.Update("awg7", func(t *storage.AWGTunnel) error {
		t.PolicyHolds = []storage.PolicyHold{{Policy: "HydraRoute", Order: 0}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	s.StopMonitoring("awg7")
	if got := fp.lists["HydraRoute"]; !reflect.DeepEqual(got, []string{"OpkgTun7", "Proxy1"}) {
		t.Fatalf("HydraRoute = %v", got)
	}
}

// Позиции пишутся в запись ДО deny: упади демон между ними — permit вернётся,
// а не потеряется.
func TestPolicyHold_HoldsWrittenBeforeDeny(t *testing.T) {
	s, _, _, _ := newKernelSensorService(t)
	fp := &fakePolicies{lists: map[string][]string{"HydraRoute": {"OpkgTun7", "Proxy1"}}}
	fp.onDeny = func(policy string) {
		for _, h := range holdsOf(t, s) {
			if h.Policy == policy {
				return
			}
		}
		t.Fatalf("deny %s раньше записи позиции", policy)
	}
	s.SetPolicies(fp)
	s.releasePolicies("awg7")
	if fp.denies != 1 {
		t.Fatalf("denies = %d", fp.denies)
	}
}

// Непустая запись не значит «снято» (ревью 27.10): возврат прошёл частично
// (A вернулся, B нет) — следующий порог обязан снять A снова и не забыть B;
// deny прошлого порога упал — следующий порог повторяет его.
func TestPolicyHold_ReleaseByLiveListNotByRecord(t *testing.T) {
	s, _, _, _ := newKernelSensorService(t)
	fp := &fakePolicies{lists: map[string][]string{
		"A": {"Proxy1", "OpkgTun7"},
		"B": {"OpkgTun7"},
	}}
	s.SetPolicies(fp)

	s.releasePolicies("awg7")
	fp.permitErr = map[string]error{"B": errors.New("rci down")}
	s.restorePolicies("awg7")
	if got := holdsOf(t, s); !reflect.DeepEqual(got, []storage.PolicyHold{{Policy: "B", Order: 0}}) {
		t.Fatalf("после частичного возврата holds = %+v", got)
	}

	s.releasePolicies("awg7")
	if got := fp.lists["A"]; !reflect.DeepEqual(got, []string{"Proxy1"}) {
		t.Fatalf("A после повторного порога = %v, туннель должен уйти снова", got)
	}
	got := holdsOf(t, s)
	want := []storage.PolicyHold{{Policy: "A", Order: 1}, {Policy: "B", Order: 0}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("holds = %+v, want %+v", got, want)
	}

	// deny упал — запись есть, туннель всё ещё в политике.
	fp.permitErr = nil
	s.restorePolicies("awg7")
	fp.denyErr = errors.New("rci down")
	s.releasePolicies("awg7")
	fp.denyErr = nil
	s.releasePolicies("awg7")
	if got := fp.lists["A"]; !reflect.DeepEqual(got, []string{"Proxy1"}) {
		t.Fatalf("A = %v: упавший deny не повторён", got)
	}
}

// Глобальное выключение мониторинга возвращает permit'ы: StopMonitoring по
// туннелям после него уже не позовут (ревью 27.10).
func TestPolicyHold_StopMonitoringAllRestores(t *testing.T) {
	s, _, _, _ := newKernelSensorService(t)
	s.monitors = map[string]*tunnelMonitor{}
	fp := &fakePolicies{lists: map[string][]string{"HydraRoute": {"Proxy1"}}}
	s.SetPolicies(fp)
	if err := s.tunnels.Update("awg7", func(t *storage.AWGTunnel) error {
		t.PolicyHolds = []storage.PolicyHold{{Policy: "HydraRoute", Order: 0}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	s.StopMonitoringAll()
	if got := fp.lists["HydraRoute"]; !reflect.DeepEqual(got, []string{"OpkgTun7", "Proxy1"}) {
		t.Fatalf("HydraRoute = %v", got)
	}
}

// Остановка сервиса (перезапуск демона) permit НЕ возвращает: мёртвый
// туннель вернулся бы в политики до порога ценой двух записей во флеш;
// позиции в записи, их вернёт первая успешная проверка после старта.
func TestPolicyHold_ServiceStopKeepsHolds(t *testing.T) {
	s, _, _, _ := newKernelSensorService(t)
	s.monitors = map[string]*tunnelMonitor{}
	s.running = true
	s.cancel = func() {}
	fp := &fakePolicies{lists: map[string][]string{"HydraRoute": {"Proxy1"}}}
	s.SetPolicies(fp)
	if err := s.tunnels.Update("awg7", func(t *storage.AWGTunnel) error {
		t.PolicyHolds = []storage.PolicyHold{{Policy: "HydraRoute", Order: 0}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	s.Stop()
	if got := fp.lists["HydraRoute"]; !reflect.DeepEqual(got, []string{"Proxy1"}) {
		t.Fatalf("Stop вернул permit: %v", got)
	}
	if len(holdsOf(t, s)) != 1 {
		t.Fatal("Stop потерял позиции")
	}
}
