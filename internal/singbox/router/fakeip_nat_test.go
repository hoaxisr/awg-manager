package router

import (
	"context"
	"errors"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// recordingSegmentNAT records the SegmentNAT calls in order so the apply/teardown
// tests can assert both the operations and their sequence.
type recordingSegmentNAT struct {
	calls []string
}

func (r *recordingSegmentNAT) SetSegmentNAT(_ context.Context, seg string) error {
	r.calls = append(r.calls, "SetSegmentNAT "+seg)
	return nil
}
func (r *recordingSegmentNAT) RemoveSegmentNAT(_ context.Context, seg string) error {
	r.calls = append(r.calls, "RemoveSegmentNAT "+seg)
	return nil
}
func (r *recordingSegmentNAT) SetStaticNAT(_ context.Context, seg, wan string) error {
	r.calls = append(r.calls, "SetStaticNAT "+seg+" "+wan)
	return nil
}
func (r *recordingSegmentNAT) RemoveStaticNAT(_ context.Context, seg, wan string) error {
	r.calls = append(r.calls, "RemoveStaticNAT "+seg+" "+wan)
	return nil
}

type fakePoolSegments struct {
	seg string
	err error
}

func (f fakePoolSegments) SegmentForPool(context.Context, string) (string, error) {
	return f.seg, f.err
}

type fakeDefaultGateway struct {
	id  string
	err error
}

func (f fakeDefaultGateway) DefaultGatewayInterface(context.Context) (string, error) {
	return f.id, f.err
}

type fakeWANLister struct {
	wans []WANInterfaceInfo
	err  error
}

func (f fakeWANLister) ListWAN(context.Context) ([]WANInterfaceInfo, error) {
	return f.wans, f.err
}

// ---- resolver ----

func TestResolveDeliverySegmentAndWAN_Autodetect(t *testing.T) {
	store := newTestSettingsStore(t, storage.SingboxRouterSettings{WANAutoDetect: true})
	s := &ServiceImpl{deps: Deps{
		Settings:         store,
		FakeIPTun:        FakeIPTunParams{DHCPPool: "_WEBADMIN"},
		DHCPPoolSegments: fakePoolSegments{seg: "Home"},
		DefaultGateway:   fakeDefaultGateway{id: "PPPoE0"},
	}}
	seg, wan, err := s.resolveDeliverySegmentAndWAN(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seg != "Home" {
		t.Errorf("seg = %q, want Home", seg)
	}
	if wan != "PPPoE0" {
		t.Errorf("wan = %q, want PPPoE0", wan)
	}
}

func TestResolveDeliverySegmentAndWAN_PinnedKernelName(t *testing.T) {
	store := newTestSettingsStore(t, storage.SingboxRouterSettings{
		WANAutoDetect: false,
		WANInterface:  "ppp0", // kernel system-name
	})
	s := &ServiceImpl{deps: Deps{
		Settings:         store,
		FakeIPTun:        FakeIPTunParams{DHCPPool: "_WEBADMIN"},
		DHCPPoolSegments: fakePoolSegments{seg: "Home"},
		WANInterfaces: fakeWANLister{wans: []WANInterfaceInfo{
			{Name: "eth3", ID: "ISP"},
			{Name: "ppp0", ID: "PPPoE0"},
		}},
	}}
	seg, wan, err := s.resolveDeliverySegmentAndWAN(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seg != "Home" {
		t.Errorf("seg = %q, want Home", seg)
	}
	if wan != "PPPoE0" {
		t.Errorf("wan = %q, want PPPoE0 (reverse kernel→NDMS map)", wan)
	}
}

func TestResolveDeliverySegmentAndWAN_UnwiredSkips(t *testing.T) {
	store := newTestSettingsStore(t, storage.SingboxRouterSettings{WANAutoDetect: true})
	s := &ServiceImpl{deps: Deps{
		Settings:  store,
		FakeIPTun: FakeIPTunParams{DHCPPool: "_WEBADMIN"},
		// DHCPPoolSegments nil → skip, no error
	}}
	seg, wan, err := s.resolveDeliverySegmentAndWAN(context.Background())
	if err != nil {
		t.Fatalf("unwired must not error, got: %v", err)
	}
	if seg != "" || wan != "" {
		t.Errorf("unwired must return empty, got seg=%q wan=%q", seg, wan)
	}
}

func TestResolveDeliverySegmentAndWAN_PinnedNotInList(t *testing.T) {
	store := newTestSettingsStore(t, storage.SingboxRouterSettings{
		WANAutoDetect: false,
		WANInterface:  "ppp9",
	})
	s := &ServiceImpl{deps: Deps{
		Settings:         store,
		FakeIPTun:        FakeIPTunParams{DHCPPool: "_WEBADMIN"},
		DHCPPoolSegments: fakePoolSegments{seg: "Home"},
		WANInterfaces:    fakeWANLister{wans: []WANInterfaceInfo{{Name: "ppp0", ID: "PPPoE0"}}},
	}}
	if _, _, err := s.resolveDeliverySegmentAndWAN(context.Background()); err == nil {
		t.Fatal("expected error for pinned WAN not in list")
	}
}

func TestResolveDeliverySegmentAndWAN_PoolError(t *testing.T) {
	store := newTestSettingsStore(t, storage.SingboxRouterSettings{WANAutoDetect: true})
	s := &ServiceImpl{deps: Deps{
		Settings:         store,
		FakeIPTun:        FakeIPTunParams{DHCPPool: "_WEBADMIN"},
		DHCPPoolSegments: fakePoolSegments{err: errors.New("boom")},
		DefaultGateway:   fakeDefaultGateway{id: "PPPoE0"},
	}}
	if _, _, err := s.resolveDeliverySegmentAndWAN(context.Background()); err == nil {
		t.Fatal("expected error when SegmentForPool fails")
	}
}

// ---- apply / teardown ----

func TestApplyStaticNAT_Order(t *testing.T) {
	rec := &recordingSegmentNAT{}
	s := &ServiceImpl{deps: Deps{SegmentNAT: rec}}
	if err := s.applyStaticNAT(context.Background(), "Home", "PPPoE0"); err != nil {
		t.Fatalf("applyStaticNAT: %v", err)
	}
	want := []string{"RemoveSegmentNAT Home", "SetStaticNAT Home PPPoE0"}
	if len(rec.calls) != len(want) {
		t.Fatalf("calls = %v, want %v", rec.calls, want)
	}
	for i := range want {
		if rec.calls[i] != want[i] {
			t.Errorf("call[%d] = %q, want %q", i, rec.calls[i], want[i])
		}
	}
}

func TestTeardownStaticNAT_Order(t *testing.T) {
	rec := &recordingSegmentNAT{}
	s := &ServiceImpl{deps: Deps{SegmentNAT: rec}}
	if err := s.teardownStaticNAT(context.Background(), "Home", "PPPoE0"); err != nil {
		t.Fatalf("teardownStaticNAT: %v", err)
	}
	want := []string{"RemoveStaticNAT Home PPPoE0", "SetSegmentNAT Home"}
	if len(rec.calls) != len(want) {
		t.Fatalf("calls = %v, want %v", rec.calls, want)
	}
	for i := range want {
		if rec.calls[i] != want[i] {
			t.Errorf("call[%d] = %q, want %q", i, rec.calls[i], want[i])
		}
	}
}
