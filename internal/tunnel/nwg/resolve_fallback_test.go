package nwg

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

func TestTrackEndpointIP_GetReturnsTracked(t *testing.T) {
	o := &OperatorNativeWG{}
	t.Cleanup(o.Close)
	if got := o.GetTrackedEndpointIP("awg10"); got != "" {
		t.Fatalf("empty operator: GetTrackedEndpointIP = %q, want \"\"", got)
	}
	o.trackEndpointIP("awg10", "1.2.3.4")
	if got := o.GetTrackedEndpointIP("awg10"); got != "1.2.3.4" {
		t.Fatalf("GetTrackedEndpointIP = %q, want 1.2.3.4", got)
	}
	if got := o.GetTrackedEndpointIP("other"); got != "" {
		t.Fatalf("unknown id: GetTrackedEndpointIP = %q, want \"\"", got)
	}
}

func newTestOperator(t *testing.T, resolveFn func(string) (string, int, error)) *OperatorNativeWG {
	t.Helper()
	o := &OperatorNativeWG{
		appLog:    logging.NewScopedLogger(nil, logging.GroupTunnel, logging.SubOps),
		resolveFn: resolveFn,
	}
	t.Cleanup(o.Close)
	return o
}

func TestResolveWithFallback_FreshSuccessTracks(t *testing.T) {
	o := newTestOperator(t, func(string) (string, int, error) { return "9.9.9.9", 51820, nil })
	stored := &storage.AWGTunnel{ID: "awg10", Peer: storage.AWGPeer{Endpoint: "host.example:51820"}}

	ip, port, err := o.resolveEndpointWithFallback(stored)
	if err != nil || ip != "9.9.9.9" || port != 51820 {
		t.Fatalf("got (%q,%d,%v), want (9.9.9.9,51820,nil)", ip, port, err)
	}
	if got := o.GetTrackedEndpointIP("awg10"); got != "9.9.9.9" {
		t.Fatalf("tracked = %q, want 9.9.9.9", got)
	}
}

func TestResolveWithFallback_RetriesThenSucceeds(t *testing.T) {
	var calls int32
	o := newTestOperator(t, func(string) (string, int, error) {
		if atomic.AddInt32(&calls, 1) < 2 {
			return "", 0, fmt.Errorf("dns timeout")
		}
		return "9.9.9.9", 51820, nil
	})
	stored := &storage.AWGTunnel{ID: "awg10", Peer: storage.AWGPeer{Endpoint: "host.example:51820"}}

	ip, _, err := o.resolveEndpointWithFallback(stored)
	if err != nil || ip != "9.9.9.9" {
		t.Fatalf("got (%q,%v), want (9.9.9.9,nil)", ip, err)
	}
	if calls < 2 {
		t.Fatalf("calls = %d, want >= 2 (retry happened)", calls)
	}
}

func TestResolveWithFallback_AllFailUsesCacheNoTrack(t *testing.T) {
	o := newTestOperator(t, func(string) (string, int, error) { return "", 0, fmt.Errorf("dns down") })
	stored := &storage.AWGTunnel{
		ID:                 "awg10",
		Peer:               storage.AWGPeer{Endpoint: "host.example:51820"},
		ResolvedEndpointIP: "5.5.5.5",
	}

	ip, port, err := o.resolveEndpointWithFallback(stored)
	if err != nil || ip != "5.5.5.5" || port != 51820 {
		t.Fatalf("got (%q,%d,%v), want (5.5.5.5,51820,nil)", ip, port, err)
	}
	if got := o.GetTrackedEndpointIP("awg10"); got != "" {
		t.Fatalf("cache fallback must NOT track; tracked = %q", got)
	}
}

func TestResolveWithFallback_AllFailNoCacheErrors(t *testing.T) {
	o := newTestOperator(t, func(string) (string, int, error) { return "", 0, fmt.Errorf("dns down") })
	stored := &storage.AWGTunnel{ID: "awg10", Peer: storage.AWGPeer{Endpoint: "host.example:51820"}}

	if _, _, err := o.resolveEndpointWithFallback(stored); err == nil {
		t.Fatal("want error when DNS fails and no cache, got nil")
	}
}

func TestResolveWithFallback_SlowAttemptTimesOut(t *testing.T) {
	o := newTestOperator(t, func(string) (string, int, error) {
		time.Sleep(resolveAttemptTimeout + 500*time.Millisecond)
		return "9.9.9.9", 51820, nil
	})
	stored := &storage.AWGTunnel{
		ID:                 "awg10",
		Peer:               storage.AWGPeer{Endpoint: "host.example:51820"},
		ResolvedEndpointIP: "5.5.5.5",
	}

	ip, _, err := o.resolveEndpointWithFallback(stored)
	if err != nil || ip != "5.5.5.5" {
		t.Fatalf("got (%q,%v), want (5.5.5.5,nil) via cache after timeouts", ip, err)
	}
}
