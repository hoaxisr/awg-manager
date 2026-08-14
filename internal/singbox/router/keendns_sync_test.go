package router

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

type fakeKeenDNSDomain struct {
	domain string
	err    error
}

func (f fakeKeenDNSDomain) KeenDNSDomain(context.Context) (string, error) {
	return f.domain, f.err
}

type fakeKeenDNSAddr struct {
	ips   []string
	err   error
	calls int
}

func (f *fakeKeenDNSAddr) KeenDNSAddrs(context.Context, string) ([]string, error) {
	f.calls++
	return f.ips, f.err
}

type keenDNSSyncCall struct {
	domain string
	ips    string
}

type recordingKeenDNSSync struct{ calls []keenDNSSyncCall }

func (r *recordingKeenDNSSync) SyncManagedKeenDNS(domain string, ips []string) error {
	r.calls = append(r.calls, keenDNSSyncCall{domain, strings.Join(ips, ",")})
	return nil
}

func newKeenDNSSyncTestSvc(sync *recordingKeenDNSSync) *ServiceImpl {
	return &ServiceImpl{
		appLog:      logging.NewScopedLogger(nil, logging.GroupRouting, logging.SubSingboxRouter),
		keenDNSSync: sync,
	}
}

func TestSyncKeenDNSRewrites_NDMSErrorKeepsExisting(t *testing.T) {
	sync := &recordingKeenDNSSync{}
	svc := newKeenDNSSyncTestSvc(sync)
	svc.keenDNSDomain = fakeKeenDNSDomain{err: errors.New("ndms down")}
	svc.keenDNSAddr = &fakeKeenDNSAddr{ips: []string{"78.47.125.180"}}

	svc.syncKeenDNSRewrites(context.Background(), storage.SingboxRouterSettings{
		BypassPresets: []string{"keendns"},
		WANAutoDetect: true,
		DeviceMode:    "policy",
	})
	if len(sync.calls) != 0 {
		t.Fatalf("NDMS error must not SyncManagedKeenDNS (would clear), got %v", sync.calls)
	}
}

func TestSyncKeenDNSRewrites_NoStaticRecordKeepsExisting(t *testing.T) {
	sync := &recordingKeenDNSSync{}
	svc := newKeenDNSSyncTestSvc(sync)
	svc.keenDNSDomain = fakeKeenDNSDomain{domain: "home.netcraze.pro"}
	svc.keenDNSAddr = &fakeKeenDNSAddr{}

	svc.syncKeenDNSRewrites(context.Background(), storage.SingboxRouterSettings{
		BypassPresets: []string{"keendns"},
		WANAutoDetect: true,
		DeviceMode:    "policy",
	})
	if len(sync.calls) != 0 {
		t.Fatalf("отсутствие статической записи не должно сносить managed, got %v", sync.calls)
	}
}

func TestSyncKeenDNSRewrites_UnbookedClears(t *testing.T) {
	sync := &recordingKeenDNSSync{}
	svc := newKeenDNSSyncTestSvc(sync)
	svc.keenDNSDomain = fakeKeenDNSDomain{domain: ""}
	svc.keenDNSAddr = &fakeKeenDNSAddr{ips: []string{"78.47.125.180"}}

	svc.syncKeenDNSRewrites(context.Background(), storage.SingboxRouterSettings{
		BypassPresets: []string{"keendns"},
		WANAutoDetect: true,
		DeviceMode:    "policy",
	})
	if len(sync.calls) != 1 || sync.calls[0] != (keenDNSSyncCall{}) {
		t.Fatalf("unbooked KeenDNS must clear managed, got %v", sync.calls)
	}
}

func TestSyncKeenDNSRewrites_HappyPath(t *testing.T) {
	sync := &recordingKeenDNSSync{}
	svc := newKeenDNSSyncTestSvc(sync)
	svc.keenDNSDomain = fakeKeenDNSDomain{domain: "Home.Netcraze.Pro."}
	svc.keenDNSAddr = &fakeKeenDNSAddr{ips: []string{"78.47.125.180"}}

	svc.syncKeenDNSRewrites(context.Background(), storage.SingboxRouterSettings{
		BypassPresets: []string{"keendns"},
		WANAutoDetect: true,
		DeviceMode:    "policy",
	})
	if len(sync.calls) != 1 {
		t.Fatalf("want 1 sync call, got %v", sync.calls)
	}
	c := sync.calls[0]
	if c.domain != "Home.Netcraze.Pro." || c.ips != "78.47.125.180" {
		t.Fatalf("unexpected call: %+v", c)
	}
	if got := svc.keenDNSBypass(); len(got) != 1 || got[0] != "78.47.125.180/32" {
		t.Fatalf("обход = %v, want [78.47.125.180/32]", got)
	}
}

func TestSyncKeenDNSRewrites_PresetOffClears(t *testing.T) {
	sync := &recordingKeenDNSSync{}
	svc := newKeenDNSSyncTestSvc(sync)
	svc.keenDNSDomain = fakeKeenDNSDomain{domain: "home.netcraze.pro"}
	svc.keenDNSAddr = &fakeKeenDNSAddr{ips: []string{"78.47.125.180"}}

	svc.syncKeenDNSRewrites(context.Background(), storage.SingboxRouterSettings{
		WANAutoDetect: true,
		DeviceMode:    "policy",
	})
	if len(sync.calls) != 1 || sync.calls[0] != (keenDNSSyncCall{}) {
		t.Fatalf("preset off must clear, got %v", sync.calls)
	}
	if got := svc.keenDNSBypass(); len(got) != 0 {
		t.Fatalf("снятый пресет обязан снять и обход, got %v", got)
	}
}

// Reconcile зовёт синк каждые 30с, а адрес сервиса KeenDNS не меняется —
// в RCI ходим не чаще keenDNSAddrTTL.
func TestSyncKeenDNSRewrites_AddrCached(t *testing.T) {
	sync := &recordingKeenDNSSync{}
	svc := newKeenDNSSyncTestSvc(sync)
	addr := &fakeKeenDNSAddr{ips: []string{"78.47.125.180"}}
	svc.keenDNSDomain = fakeKeenDNSDomain{domain: "home.netcraze.pro"}
	svc.keenDNSAddr = addr

	sr := storage.SingboxRouterSettings{
		BypassPresets: []string{"keendns"},
		WANAutoDetect: true,
		DeviceMode:    "policy",
	}
	svc.syncKeenDNSRewrites(context.Background(), sr)
	svc.syncKeenDNSRewrites(context.Background(), sr)
	if addr.calls != 1 {
		t.Fatalf("запросов статических записей = %d, want 1", addr.calls)
	}
}

// Сбой RCI не должен ронять обход и перезапись: отдаём last-good того же имени.
func TestSyncKeenDNSRewrites_AddrErrorKeepsLastGood(t *testing.T) {
	sync := &recordingKeenDNSSync{}
	svc := newKeenDNSSyncTestSvc(sync)
	addr := &fakeKeenDNSAddr{ips: []string{"78.47.125.180"}}
	svc.keenDNSDomain = fakeKeenDNSDomain{domain: "home.netcraze.pro"}
	svc.keenDNSAddr = addr

	sr := storage.SingboxRouterSettings{
		BypassPresets: []string{"keendns"},
		WANAutoDetect: true,
		DeviceMode:    "policy",
	}
	svc.syncKeenDNSRewrites(context.Background(), sr)

	// Протухаем кэш и роняем провайдера.
	svc.keenDNSMu.Lock()
	svc.keenDNSAddrAt = time.Now().Add(-2 * keenDNSAddrTTL)
	svc.keenDNSMu.Unlock()
	addr.ips, addr.err = nil, errors.New("rci down")

	svc.syncKeenDNSRewrites(context.Background(), sr)
	if len(sync.calls) != 2 || sync.calls[1].ips != "78.47.125.180" {
		t.Fatalf("last-good должен пережить сбой RCI, got %v", sync.calls)
	}
	if got := svc.keenDNSBypass(); len(got) != 1 || got[0] != "78.47.125.180/32" {
		t.Fatalf("обход = %v, want [78.47.125.180/32]", got)
	}
}

// Адрес KeenDNS приходит с роутера, а не из настроек: его появление обязано
// переустановить правила, иначе обход доедет только по ручному Enable (#729).
func TestReconcileInstalled_KeenDNSCIDRChangeReinstalls(t *testing.T) {
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
		currentMark:         "0xffffaaa",
		currentWANIPs:       []string{"203.0.113.207/32"},
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
	if !slices.Equal(svc.currentKeenDNSCIDRs, []string{"78.47.125.180/32"}) {
		t.Fatalf("currentKeenDNSCIDRs = %v", svc.currentKeenDNSCIDRs)
	}

	if err := svc.reconcileInstalled(context.Background(), sr); err != nil {
		t.Fatalf("reconcileInstalled: %v", err)
	}
	if restoreCalls != 1 {
		t.Fatalf("неизменный адрес не должен переустанавливать правила, got %d", restoreCalls)
	}
}
