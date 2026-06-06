package downloader

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeTunnelPorts struct {
	ports map[string]int
}

func (f *fakeTunnelPorts) ListenPortByTag(_ context.Context, tag string) (int, bool) {
	p, ok := f.ports[tag]
	return p, ok && p > 0
}

type fakeSubPorts struct {
	ports map[string]int
}

func (f *fakeSubPorts) ListenPortBySelectorTag(_ context.Context, tag string) (int, bool) {
	p, ok := f.ports[tag]
	return p, ok && p > 0
}

type fakeSingboxRuntime struct {
	running bool
	ready   bool
}

func (f *fakeSingboxRuntime) IsRunning() bool { return f.running }
func (f *fakeSingboxRuntime) IsReady() bool  { return f.running && f.ready }

type fakeAWGEgress struct {
	dns map[string][]string
}

func (f *fakeAWGEgress) DNSForTag(_ context.Context, tag string) []string {
	if f == nil || f.dns == nil {
		return nil
	}
	return f.dns[tag]
}

func proxyPortAlwaysOpen(_ string, _ int) bool { return true }

func proxyPortAlwaysClosed(_ string, _ int) bool { return false }

func TestTransportResolver_AWG_Bind(t *testing.T) {
	sysNet := t.TempDir()
	if err := os.Mkdir(filepath.Join(sysNet, "opkgtun1"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := NewTransportResolver(TransportResolverDeps{
		SysClassNet: sysNet,
		AWGEgress: &fakeAWGEgress{dns: map[string][]string{
			"awg-1": {"10.0.0.1", "1.1.1.1"},
		}},
	})

	spec, err := r.Resolve(context.Background(), Outbound{
		Tag: "awg-1", Kind: "awg", Detail: "opkgtun1",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if spec.Mode != TransportModeBind || spec.Interface != "opkgtun1" {
		t.Fatalf("spec = %+v, want bind opkgtun1", spec)
	}
	if len(spec.DNSServers) != 2 || spec.DNSServers[0] != "10.0.0.1" {
		t.Fatalf("DNSServers = %+v, want tunnel DNS", spec.DNSServers)
	}
}

func TestTransportResolver_AWG_MissingIface(t *testing.T) {
	r := NewTransportResolver(TransportResolverDeps{SysClassNet: t.TempDir()})
	_, err := r.Resolve(context.Background(), Outbound{Tag: "awg-1", Kind: "awg", Detail: "missing0"})
	if err == nil || !strings.Contains(err.Error(), "not present") {
		t.Fatalf("expected missing iface error, got %v", err)
	}
	if RouteErrorCode(err) != ErrCodeAWGDown {
		t.Fatalf("RouteErrorCode = %q, want %q", RouteErrorCode(err), ErrCodeAWGDown)
	}
}

func TestTransportResolver_Singbox_ProxyPort(t *testing.T) {
	r := NewTransportResolver(TransportResolverDeps{
		Tunnels:       &fakeTunnelPorts{ports: map[string]int{"DE": 1080}},
		Singbox:       &fakeSingboxRuntime{running: true, ready: true},
		ProxyPortOpen: proxyPortAlwaysOpen,
	})
	spec, err := r.Resolve(context.Background(), Outbound{Tag: "DE", Kind: "singbox"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if spec.Mode != TransportModeProxy || spec.ProxyPort != 1080 {
		t.Fatalf("spec = %+v, want proxy :1080", spec)
	}
}

func TestTransportResolver_Singbox_ProxyPortNotListening(t *testing.T) {
	r := NewTransportResolver(TransportResolverDeps{
		Tunnels:       &fakeTunnelPorts{ports: map[string]int{"DE": 1080}},
		Singbox:       &fakeSingboxRuntime{running: true, ready: true},
		ProxyPortOpen: proxyPortAlwaysClosed,
	})
	_, err := r.Resolve(context.Background(), Outbound{Tag: "DE", Kind: "singbox"})
	if err == nil || !strings.Contains(err.Error(), "proxy port not listening") {
		t.Fatalf("expected proxy port not listening error, got %v", err)
	}
	if RouteErrorCode(err) != ErrCodeSingboxNotReady {
		t.Fatalf("RouteErrorCode = %q, want %q", RouteErrorCode(err), ErrCodeSingboxNotReady)
	}
}

func TestTransportResolver_Singbox_NotReady(t *testing.T) {
	r := NewTransportResolver(TransportResolverDeps{
		Tunnels:       &fakeTunnelPorts{ports: map[string]int{"DE": 1080}},
		Singbox:       &fakeSingboxRuntime{running: true, ready: false},
		ProxyPortOpen: proxyPortAlwaysOpen,
	})
	_, err := r.Resolve(context.Background(), Outbound{Tag: "DE", Kind: "singbox"})
	if err == nil || RouteErrorCode(err) != ErrCodeSingboxNotReady {
		t.Fatalf("expected not ready error, got %v", err)
	}
}

func TestTransportResolver_Singbox_NotRunning(t *testing.T) {
	r := NewTransportResolver(TransportResolverDeps{
		Tunnels: &fakeTunnelPorts{ports: map[string]int{"DE": 1080}},
		Singbox: &fakeSingboxRuntime{running: false},
	})
	_, err := r.Resolve(context.Background(), Outbound{Tag: "DE", Kind: "singbox"})
	if err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("expected not running error, got %v", err)
	}
	if RouteErrorCode(err) != ErrCodeSingboxNotRunning {
		t.Fatalf("RouteErrorCode = %q, want %q", RouteErrorCode(err), ErrCodeSingboxNotRunning)
	}
}

func TestTransportResolver_Subscription_ProxyPort(t *testing.T) {
	r := NewTransportResolver(TransportResolverDeps{
		Subs:          &fakeSubPorts{ports: map[string]int{"sub-abc": 11001}},
		Singbox:       &fakeSingboxRuntime{running: true, ready: true},
		ProxyPortOpen: proxyPortAlwaysOpen,
	})
	spec, err := r.Resolve(context.Background(), Outbound{Tag: "sub-abc", Kind: "subscription"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if spec.Mode != TransportModeProxy || spec.ProxyPort != 11001 {
		t.Fatalf("spec = %+v, want proxy :11001", spec)
	}
}

func TestTransportResolver_AWG_AvailableWithoutSingbox(t *testing.T) {
	sysNet := t.TempDir()
	if err := os.Mkdir(filepath.Join(sysNet, "nwg0"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := NewTransportResolver(TransportResolverDeps{SysClassNet: sysNet})
	ob := Outbound{Tag: "awg-sys-1", Kind: "awg", Detail: "nwg0"}
	if !r.IsAvailable(context.Background(), ob) {
		t.Fatal("AWG outbound should be available without sing-box")
	}
}
