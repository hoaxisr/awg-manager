package router

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// newFakeIPSettingsStore builds a SettingsStore seeded with RoutingMode=
// fakeip-tun and a provisioned FakeIPState at the given index, mirroring the
// state the lifecycle persists once a fakeip-tun Enable has provisioned the tun.
func newFakeIPSettingsStore(t *testing.T, index int, provisioned bool) *storage.SettingsStore {
	t.Helper()
	store := newTestSettingsStore(t, storage.SingboxRouterSettings{
		RoutingMode:   "fakeip-tun",
		WANAutoDetect: true,
	})
	if provisioned {
		if err := store.SetFakeIPState(&storage.FakeIPState{
			Provisioned: true,
			Index:       index,
			Inet4Range:  "198.18.0.0/15",
			Inet6Range:  "fc00::/18",
		}); err != nil {
			t.Fatalf("SetFakeIPState: %v", err)
		}
	}
	return store
}

// stubTunReadyProbe / stubFakeIPDNSProbe / stubFakeIPPoolRoutePresent override
// the fakeip readiness seams for the test duration (save/restore via Cleanup).
func stubTunReadyProbe(t *testing.T, fn func(string) bool) {
	t.Helper()
	old := tunReadyProbe
	tunReadyProbe = fn
	t.Cleanup(func() { tunReadyProbe = old })
}

func stubFakeIPDNSProbe(t *testing.T, fn func(context.Context, string, netip.Prefix) bool) {
	t.Helper()
	old := fakeIPDNSProbe
	fakeIPDNSProbe = fn
	t.Cleanup(func() { fakeIPDNSProbe = old })
}

func stubFakeIPPoolRoutePresent(t *testing.T, fn func(string, netip.Prefix) bool) {
	t.Helper()
	old := fakeIPPoolRoutePresent
	fakeIPPoolRoutePresent = fn
	t.Cleanup(func() { fakeIPPoolRoutePresent = old })
}

// ---------------------------------------------------------------------------
// waitForSingbox — fakeip-tun branch
// ---------------------------------------------------------------------------

// The readiness gate for fakeip-tun is process + tun carrier ONLY — the live
// .2→fakeip DNS probe was demoted to a best-effort post-readiness confirm (it
// tripped on resolv.conf attempts:1, falsely timing Enable out). So readiness
// must turn true on carrier alone, and the DNS probe seam must NEVER be consulted
// by the gate (the stub fails the test if it is).
func TestWaitForSingbox_FakeIP_ReadyWhenCarrier(t *testing.T) {
	singbox := newTestSingbox(t)
	singbox.isRunningFn = func() (bool, int) { return true, 1234 }
	svc := newTestService(t, Deps{
		Singbox:   singbox,
		Settings:  newFakeIPSettingsStore(t, 3, true),
		FakeIPTun: DefaultFakeIPTunParams(),
	})

	var gotIface string
	stubTunReadyProbe(t, func(iface string) bool { gotIface = iface; return true })
	// The DNS probe is no longer in the readiness gate — it must not be called.
	stubFakeIPDNSProbe(t, func(context.Context, string, netip.Prefix) bool {
		t.Fatal("fakeip DNS probe must not be consulted by the readiness gate (it is best-effort post-readiness)")
		return false
	})
	// The tproxy socket probe must NEVER be consulted in fakeip-tun mode.
	stubListeningProbe(t, func() bool {
		t.Fatal("tproxy listening probe must not be called in fakeip-tun mode")
		return false
	})

	if err := svc.waitForSingbox(context.Background(), 2*time.Second); err != nil {
		t.Fatalf("waitForSingbox (fakeip ready on carrier): %v", err)
	}
	if gotIface != "opkgtun3" {
		t.Errorf("tun probe iface = %q, want opkgtun3", gotIface)
	}
}

func TestWaitForSingbox_FakeIP_TimesOutWhenCarrierDown(t *testing.T) {
	singbox := newTestSingbox(t)
	singbox.isRunningFn = func() (bool, int) { return true, 1234 }
	svc := newTestService(t, Deps{
		Singbox:   singbox,
		Settings:  newFakeIPSettingsStore(t, 0, true),
		FakeIPTun: DefaultFakeIPTunParams(),
	})

	stubTunReadyProbe(t, func(string) bool { return false })
	stubFakeIPDNSProbe(t, func(context.Context, string, netip.Prefix) bool {
		t.Fatal("DNS probe must not run in the gate at all")
		return true
	})

	if err := svc.waitForSingbox(context.Background(), 250*time.Millisecond); err == nil {
		t.Fatal("expected timeout when tun carrier never comes up")
	}
}

func TestWaitForSingbox_FakeIP_TimesOutWhenNotProvisioned(t *testing.T) {
	singbox := newTestSingbox(t)
	singbox.isRunningFn = func() (bool, int) { return true, 1234 }
	svc := newTestService(t, Deps{
		Singbox:   singbox,
		Settings:  newFakeIPSettingsStore(t, 0, false), // mode=fakeip-tun, but no FakeIPState
		FakeIPTun: DefaultFakeIPTunParams(),
	})

	stubTunReadyProbe(t, func(string) bool { return true })

	if err := svc.waitForSingbox(context.Background(), 250*time.Millisecond); err == nil {
		t.Fatal("expected timeout when fakeip inputs are unresolvable (not provisioned)")
	}
}

// ---------------------------------------------------------------------------
// GetStatus.Active — fakeip-tun branch
// ---------------------------------------------------------------------------

func newFakeIPStatusService(t *testing.T, index int) *ServiceImpl {
	t.Helper()
	singbox := newTestSingbox(t)
	singbox.isRunningFn = func() (bool, int) { return true, 1234 }
	return newTestService(t, Deps{
		Singbox:   singbox,
		Settings:  newFakeIPSettingsStore(t, index, true),
		FakeIPTun: DefaultFakeIPTunParams(),
		IPTables:  newStubIPTables(func(context.Context, string) error { return nil }),
	})
}

func TestGetStatus_FakeIP_ActiveWhenCarrierAndRoute(t *testing.T) {
	svc := newFakeIPStatusService(t, 2)
	stubTunReadyProbe(t, func(string) bool { return true })
	var gotIface string
	stubFakeIPPoolRoutePresent(t, func(iface string, n netip.Prefix) bool {
		gotIface = iface
		return n.Contains(netip.MustParseAddr("198.18.0.5"))
	})
	stubListeningProbe(t, func() bool {
		t.Fatal("tproxy listening probe must not be called for fakeip-tun status")
		return false
	})

	st, err := svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if !st.Active {
		t.Error("Active must be true when process up + tun carrier + pool route present")
	}
	if gotIface != "opkgtun2" {
		t.Errorf("pool-route probe iface = %q, want opkgtun2", gotIface)
	}
}

func TestGetStatus_FakeIP_InactiveWhenRouteMissing(t *testing.T) {
	svc := newFakeIPStatusService(t, 0)
	stubTunReadyProbe(t, func(string) bool { return true })
	stubFakeIPPoolRoutePresent(t, func(string, netip.Prefix) bool { return false })

	st, err := svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.Active {
		t.Error("Active must be false when the fakeip pool auto-route is absent")
	}
}

func TestGetStatus_FakeIP_InactiveWhenCarrierDown(t *testing.T) {
	svc := newFakeIPStatusService(t, 0)
	stubTunReadyProbe(t, func(string) bool { return false })
	stubFakeIPPoolRoutePresent(t, func(string, netip.Prefix) bool {
		t.Fatal("pool-route probe must not run while tun carrier is down")
		return true
	})

	st, err := svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.Active {
		t.Error("Active must be false when the tun carrier is down")
	}
}

func TestGetStatus_FakeIP_InactiveWhenProcessDown(t *testing.T) {
	svc := newFakeIPStatusService(t, 0)
	svc.deps.Singbox.(*fakeSingbox).isRunningFn = func() (bool, int) { return false, 0 }
	stubTunReadyProbe(t, func(string) bool { return true })
	stubFakeIPPoolRoutePresent(t, func(string, netip.Prefix) bool { return true })

	st, err := svc.GetStatus(context.Background())
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.Active {
		t.Error("Active must be false when sing-box process is down")
	}
}
