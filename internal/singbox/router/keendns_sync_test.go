package router

import (
	"context"
	"errors"
	"testing"

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

type fakeLANIP struct{ ip string }

func (f fakeLANIP) LANIPv4() string { return f.ip }

type keenDNSSyncCall struct {
	domain string
	lanIP  string
}

type recordingKeenDNSSync struct{ calls []keenDNSSyncCall }

func (r *recordingKeenDNSSync) SyncManagedKeenDNS(domain, lanIP string) error {
	r.calls = append(r.calls, keenDNSSyncCall{domain, lanIP})
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
	svc.keenDNSLAN = fakeLANIP{ip: "192.168.1.1"}

	svc.syncKeenDNSRewrites(context.Background(), storage.SingboxRouterSettings{
		BypassPresets: []string{"keendns"},
		WANAutoDetect: true,
		DeviceMode:    "policy",
	})
	if len(sync.calls) != 0 {
		t.Fatalf("NDMS error must not SyncManagedKeenDNS (would clear), got %v", sync.calls)
	}
}

func TestSyncKeenDNSRewrites_EmptyLANKeepsExisting(t *testing.T) {
	sync := &recordingKeenDNSSync{}
	svc := newKeenDNSSyncTestSvc(sync)
	svc.keenDNSDomain = fakeKeenDNSDomain{domain: "home.netcraze.pro"}
	svc.keenDNSLAN = fakeLANIP{ip: ""}

	svc.syncKeenDNSRewrites(context.Background(), storage.SingboxRouterSettings{
		BypassPresets: []string{"keendns"},
		WANAutoDetect: true,
		DeviceMode:    "policy",
	})
	if len(sync.calls) != 0 {
		t.Fatalf("empty LAN must not clear managed rewrites, got %v", sync.calls)
	}
}

func TestSyncKeenDNSRewrites_UnbookedClears(t *testing.T) {
	sync := &recordingKeenDNSSync{}
	svc := newKeenDNSSyncTestSvc(sync)
	svc.keenDNSDomain = fakeKeenDNSDomain{domain: ""}
	svc.keenDNSLAN = fakeLANIP{ip: "192.168.1.1"}

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
	svc.keenDNSLAN = fakeLANIP{ip: "192.168.1.1"}

	svc.syncKeenDNSRewrites(context.Background(), storage.SingboxRouterSettings{
		BypassPresets: []string{"keendns"},
		WANAutoDetect: true,
		DeviceMode:    "policy",
	})
	if len(sync.calls) != 1 {
		t.Fatalf("want 1 sync call, got %v", sync.calls)
	}
	c := sync.calls[0]
	if c.domain != "Home.Netcraze.Pro." || c.lanIP != "192.168.1.1" {
		t.Fatalf("unexpected call: %+v", c)
	}
}

func TestSyncKeenDNSRewrites_PresetOffClears(t *testing.T) {
	sync := &recordingKeenDNSSync{}
	svc := newKeenDNSSyncTestSvc(sync)
	svc.keenDNSDomain = fakeKeenDNSDomain{domain: "home.netcraze.pro"}
	svc.keenDNSLAN = fakeLANIP{ip: "192.168.1.1"}

	svc.syncKeenDNSRewrites(context.Background(), storage.SingboxRouterSettings{
		WANAutoDetect: true,
		DeviceMode:    "policy",
	})
	if len(sync.calls) != 1 || sync.calls[0] != (keenDNSSyncCall{}) {
		t.Fatalf("preset off must clear, got %v", sync.calls)
	}
}
