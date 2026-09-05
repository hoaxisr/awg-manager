package router

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

type fakeKeenDNSInfo struct {
	fqdn  string
	addrs []string
	err   error
	calls int
}

func (f *fakeKeenDNSInfo) KeenDNSInfo(context.Context) (string, []string, error) {
	f.calls++
	return f.fqdn, f.addrs, f.err
}

type keenDNSSyncCall struct {
	on    bool
	extra string
}

type recordingKeenDNSSync struct{ calls []keenDNSSyncCall }

func (r *recordingKeenDNSSync) SetKeenDNSEnabled(on bool, extraDomain string) error {
	r.calls = append(r.calls, keenDNSSyncCall{on, extraDomain})
	return nil
}

func newKeenDNSSyncTestSvc(sync *recordingKeenDNSSync) *ServiceImpl {
	return &ServiceImpl{
		appLog:      logging.NewScopedLogger(nil, logging.GroupRouting, logging.SubSingboxRouter),
		keenDNSSync: sync,
	}
}

func keenDNSPresetSettings(on bool) storage.SingboxRouterSettings {
	sr := storage.SingboxRouterSettings{WANAutoDetect: true, DeviceMode: "policy"}
	if on {
		sr.BypassPresets = []string{"keendns"}
	}
	return sr
}

func TestSyncKeenDNSPreset_HappyPath(t *testing.T) {
	sync := &recordingKeenDNSSync{}
	svc := newKeenDNSSyncTestSvc(sync)
	svc.keenDNSInfoProv = &fakeKeenDNSInfo{
		fqdn:  "impod.netcraze.pro",
		addrs: []string{"78.47.125.180", "91.144.142.72"},
	}

	svc.syncKeenDNSPreset(context.Background(), keenDNSPresetSettings(true))
	if len(sync.calls) != 1 || sync.calls[0] != (keenDNSSyncCall{true, "impod.netcraze.pro"}) {
		t.Fatalf("вызовы синка = %v", sync.calls)
	}
	want := []string{"78.47.125.180/32", "91.144.142.72/32"}
	if got := svc.keenDNSBypass(); !slices.Equal(got, want) {
		t.Fatalf("обход = %v, want %v", got, want)
	}
}

func TestSyncKeenDNSPreset_PresetOffClears(t *testing.T) {
	sync := &recordingKeenDNSSync{}
	svc := newKeenDNSSyncTestSvc(sync)
	svc.keenDNSInfoProv = &fakeKeenDNSInfo{fqdn: "impod.netcraze.pro", addrs: []string{"78.47.125.180"}}
	svc.setKeenDNSBypass([]string{"78.47.125.180"})

	svc.syncKeenDNSPreset(context.Background(), keenDNSPresetSettings(false))
	if len(sync.calls) != 1 || sync.calls[0] != (keenDNSSyncCall{false, ""}) {
		t.Fatalf("снятый пресет обязан снять блок, got %v", sync.calls)
	}
	if got := svc.keenDNSBypass(); len(got) != 0 {
		t.Fatalf("снятый пресет обязан снять и обход, got %v", got)
	}
}

// Правило DNS нужно и без данных с роутера: порталы my.keenetic.net роутер
// обслуживает всегда, а вот обход ставить не из чего.
func TestSyncKeenDNSPreset_NDMSErrorStillEnablesRule(t *testing.T) {
	sync := &recordingKeenDNSSync{}
	svc := newKeenDNSSyncTestSvc(sync)
	svc.keenDNSInfoProv = &fakeKeenDNSInfo{err: errors.New("ndms down")}

	svc.syncKeenDNSPreset(context.Background(), keenDNSPresetSettings(true))
	if len(sync.calls) != 1 || sync.calls[0] != (keenDNSSyncCall{true, ""}) {
		t.Fatalf("вызовы синка = %v", sync.calls)
	}
	if got := svc.keenDNSBypass(); len(got) != 0 {
		t.Fatalf("обход без данных = %v, want пусто", got)
	}
}

// Сбой RCI и пустой ответ не должны сносить уже установленный обход: иначе
// разовая ошибка на 5 минут возвращает issue #729.
func TestSyncKeenDNSPreset_KeepsLastGoodBypass(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail func(*fakeKeenDNSInfo)
	}{
		{"сбой RCI", func(f *fakeKeenDNSInfo) { f.addrs, f.err = nil, errors.New("rci down") }},
		{"пустой ответ", func(f *fakeKeenDNSInfo) { f.addrs = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sync := &recordingKeenDNSSync{}
			svc := newKeenDNSSyncTestSvc(sync)
			info := &fakeKeenDNSInfo{fqdn: "impod.netcraze.pro", addrs: []string{"78.47.125.180"}}
			svc.keenDNSInfoProv = info
			sr := keenDNSPresetSettings(true)
			svc.syncKeenDNSPreset(context.Background(), sr)

			svc.keenDNSMu.Lock()
			svc.keenDNSInfoAt = time.Now().Add(-2 * keenDNSInfoTTL)
			svc.keenDNSMu.Unlock()
			tc.fail(info)

			svc.syncKeenDNSPreset(context.Background(), sr)
			want := []string{"78.47.125.180/32"}
			if got := svc.keenDNSBypass(); !slices.Equal(got, want) {
				t.Fatalf("обход = %v, want %v", got, want)
			}
		})
	}
}

// Reconcile зовёт синк каждые 30с, а адреса KeenDNS не меняются — в RCI
// ходим не чаще keenDNSInfoTTL.
func TestSyncKeenDNSPreset_InfoCached(t *testing.T) {
	sync := &recordingKeenDNSSync{}
	svc := newKeenDNSSyncTestSvc(sync)
	info := &fakeKeenDNSInfo{fqdn: "impod.netcraze.pro", addrs: []string{"78.47.125.180"}}
	svc.keenDNSInfoProv = info

	sr := keenDNSPresetSettings(true)
	svc.syncKeenDNSPreset(context.Background(), sr)
	svc.syncKeenDNSPreset(context.Background(), sr)
	if info.calls != 1 {
		t.Fatalf("запросов к роутеру = %d, want 1", info.calls)
	}
}

// Адрес KeenDNS приходит с роутера, а не из настроек: его появление обязано
// переустановить правила, иначе обход доедет только по ручному Enable (#729).
func TestReconcileInstalled_KeenDNSCIDRChangeReinstalls(t *testing.T) {
	stubNoLANBridges(t)
	restoreCalls := 0
	ipt := newStubIPTables(func(_ context.Context, _ string) error {
		restoreCalls++
		return nil
	})
	stubListeningProbe(t, func() bool { return true })
	svc := &ServiceImpl{
		deps: Deps{
			Policies:           &fakeAccessPolicyProvider{mark: "0xffffaaa"},
			IPTables:           ipt,
			WANIPCollector:     &fakeWANIPCollector{ips: []string{"203.0.113.207/32"}},
			Singbox:            newReadyTestSingbox(t),
			NetfilterPreflight: func(context.Context) error { return nil },
		},
		appliedSpec:         &RestoreInputSpec{PolicyMark: "0xffffaaa", WANIPs: []string{"203.0.113.207/32"}},
		netfilterStateKnown: true,
	}
	sr := storage.SingboxRouterSettings{Enabled: true, PolicyName: "Policy0", WANAutoDetect: true}

	if err := svc.reconcileInstalled(context.Background(), sr); err != nil {
		t.Fatalf("reconcileInstalled: %v", err)
	}
	if restoreCalls != 0 {
		t.Fatalf("устойчивое состояние не должно переустанавливать правила, got %d", restoreCalls)
	}

	svc.setKeenDNSBypass([]string{"78.47.125.180"})
	if err := svc.reconcileInstalled(context.Background(), sr); err != nil {
		t.Fatalf("reconcileInstalled: %v", err)
	}
	if restoreCalls != 1 {
		t.Fatalf("появление адреса KeenDNS обязано переустановить правила, got %d", restoreCalls)
	}
	if !slices.Contains(svc.appliedSpec.BypassCIDRs, "78.47.125.180/32") {
		t.Fatalf("применённые bypass-CIDR = %v", svc.appliedSpec.BypassCIDRs)
	}

	if err := svc.reconcileInstalled(context.Background(), sr); err != nil {
		t.Fatalf("reconcileInstalled: %v", err)
	}
	if restoreCalls != 1 {
		t.Fatalf("неизменный адрес не должен переустанавливать правила, got %d", restoreCalls)
	}
}
