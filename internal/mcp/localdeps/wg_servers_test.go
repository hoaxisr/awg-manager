package localdeps

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/managed"
	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

func (f *fakeManaged) Create(_ context.Context, req managed.CreateServerRequest) (*storage.ManagedServer, error) {
	f.createReq = &req
	sv := storage.ManagedServer{InterfaceName: "Wireguard7", Address: req.Address, Mask: req.Mask, ListenPort: req.ListenPort, NATMode: "full", Description: req.Description}
	if f.createErr != nil && f.createErr != managed.ErrASCUnverified {
		return nil, f.createErr
	}
	f.servers = append(f.servers, sv)
	return &sv, f.createErr
}
func (f *fakeManaged) Update(_ context.Context, id string, req managed.UpdateServerRequest) error {
	f.updateReq = &req
	return nil
}
func (f *fakeManaged) SetEnabled(_ context.Context, id string, on bool) error {
	f.calls = append(f.calls, "enabled "+id)
	return nil
}
func (f *fakeManaged) SetNATMode(_ context.Context, id, mode string) error {
	f.calls = append(f.calls, "nat "+mode)
	return nil
}
func (f *fakeManaged) SetLANSegments(_ context.Context, id string, segs []string) error {
	f.calls = append(f.calls, "lan "+strings.Join(segs, ","))
	return nil
}
func (f *fakeManaged) ListLANSegments(context.Context) ([]managed.LANSegmentDTO, error) {
	return []managed.LANSegmentDTO{{Name: "Bridge0", Label: "Home", Subnet: "192.168.1.0/24"}}, nil
}
func (f *fakeManaged) SetPolicy(_ context.Context, id, policy string) error {
	if f.policyErr != nil {
		return f.policyErr
	}
	f.calls = append(f.calls, "policy "+policy)
	return nil
}
func (f *fakeManaged) ListPolicies(context.Context) ([]managed.PolicyOption, error) {
	return []managed.PolicyOption{{ID: "Policy0"}}, nil
}
func (f *fakeManaged) SuggestAddress(context.Context) (string, string, error) {
	return "10.66.0.1", "24", nil
}
func (f *fakeManaged) GetASCParams(context.Context, string) (json.RawMessage, error) {
	return f.asc, nil
}
func (f *fakeManaged) SetASCParams(_ context.Context, id string, p json.RawMessage) error {
	f.ascSet = p
	return f.ascErr
}
func (f *fakeManaged) InvalidateCache(id string) { f.invalidated = append(f.invalidated, id) }

func serverWritesHarness() *fakeManaged {
	return &fakeManaged{servers: []storage.ManagedServer{{InterfaceName: "Wireguard3", Address: "10.8.0.1", Mask: "24", ListenPort: 51820, NATMode: "full", Description: "Home", DNS: "1.1.1.1"}}}
}

// TestLocal_CreateWireguardServer — подсеть по умолчанию берётся из
// SuggestAddress, как в форме; ErrASCUnverified — сервер создан, это
// предупреждение, а не отказ.
func TestLocal_CreateWireguardServer(t *testing.T) {
	m := serverWritesHarness()
	l := New(Config{Managed: m})
	got, warnings, err := l.CreateWireguardServer(context.Background(), mcpsrv.WireguardServerInput{ListenPort: 51900, AWG3: true})
	if err != nil || m.createReq.Address != "10.66.0.1" || m.createReq.Mask != "24" || m.createReq.Mode != "3.1" || got.ID != "Wireguard7" || len(warnings) != 0 {
		t.Fatalf("got %+v, %v, %v, req %+v", got, warnings, err, m.createReq)
	}
	if len(m.invalidated) != 1 {
		t.Fatal("a write must drop the server's NDMS cache, as REST does")
	}
	m.createErr = managed.ErrASCUnverified
	got, warnings, err = l.CreateWireguardServer(context.Background(), mcpsrv.WireguardServerInput{ListenPort: 51901, Address: "10.70.0.1", Mask: "24"})
	if err != nil || got.ID == "" || len(warnings) != 1 || m.createReq.Address != "10.70.0.1" {
		t.Fatalf("an unverified obfuscation is a warning: %+v, %v, %v", got, warnings, err)
	}
}

// TestLocal_UpdateWireguardServerMergesTheRequiredFields — Update требует
// адрес, маску и порт всегда: они берутся из записи, если их не меняли.
func TestLocal_UpdateWireguardServerMergesTheRequiredFields(t *testing.T) {
	m := serverWritesHarness()
	l := New(Config{Managed: m})
	if _, err := l.UpdateWireguardServer(context.Background(), mcpsrv.WireguardServerPatch{ID: "Wireguard3", Description: ptr("Office")}); err != nil {
		t.Fatal(err)
	}
	r := m.updateReq
	if r.Address != "10.8.0.1" || r.Mask != "24" || r.ListenPort != 51820 || *r.Description != "Office" || r.DNS != nil {
		t.Fatalf("req = %+v", r)
	}
	if _, err := l.UpdateWireguardServer(context.Background(), mcpsrv.WireguardServerPatch{ID: "Wireguard9", MTU: ptr(1400)}); err == nil {
		t.Fatal("an unknown server must be refused")
	}
}

func TestLocal_SetWireguardServerAccess(t *testing.T) {
	m := serverWritesHarness()
	l := New(Config{Managed: m})
	segs := []string{"Bridge0"}
	if _, err := l.SetWireguardServerAccess(context.Background(), mcpsrv.WireguardServerAccess{ID: "Wireguard3", NATMode: ptr("internet-only"), LANSegments: &segs, Policy: ptr("none")}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(m.calls, "|") != "nat internet-only|lan Bridge0|policy none" {
		t.Fatalf("calls = %v", m.calls)
	}
	bad := []string{"Guest9"}
	m.calls, m.invalidated = nil, nil
	if _, err := l.SetWireguardServerAccess(context.Background(), mcpsrv.WireguardServerAccess{ID: "Wireguard3", NATMode: ptr("none"), LANSegments: &bad}); err == nil || len(m.calls) != 0 {
		t.Fatalf("an unknown LAN segment must be refused before any write: %v, %v", err, m.calls)
	}
	m.policyErr = errors.New("rci: busy")
	if _, err := l.SetWireguardServerAccess(context.Background(), mcpsrv.WireguardServerAccess{ID: "Wireguard3", NATMode: ptr("none"), Policy: ptr("Policy0")}); err == nil ||
		!strings.Contains(err.Error(), "NAT was changed") || len(m.invalidated) != 1 {
		t.Fatalf("a write that failed after another succeeded must say so and drop the cache: %v, %v", err, m.invalidated)
	}
}

// TestLocal_SetWireguardServerObfuscationMerges — параметры ложатся поверх
// текущих; заголовки — строки в JSON NDMS, размеры — целые.
func TestLocal_SetWireguardServerObfuscationMerges(t *testing.T) {
	m := serverWritesHarness()
	m.asc = json.RawMessage(`{"jc":4,"jmin":40,"jmax":70,"h1":"1","h2":"2"}`)
	l := New(Config{Managed: m})
	if _, err := l.SetWireguardServerObfuscation(context.Background(), "Wireguard3", map[string]any{"jc": float64(6), "h1": float64(5), "s1": "20"}); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(m.ascSet, &got); err != nil {
		t.Fatal(err)
	}
	if got["jc"] != float64(6) || got["jmin"] != float64(40) || got["h1"] != "5" || got["h2"] != "2" || got["s1"] != float64(20) {
		t.Fatalf("asc = %v", got)
	}
	for _, ascErr := range []error{managed.ErrASCNotApplied, managed.ErrASCUnverified} {
		m.ascErr, m.invalidated = ascErr, nil
		if _, err := l.SetWireguardServerObfuscation(context.Background(), "Wireguard3", map[string]any{"jc": float64(7)}); err == nil || len(m.invalidated) != 1 {
			t.Fatalf("%v: part may have applied, so the cache is dropped as REST does: %v, %v", ascErr, err, m.invalidated)
		}
	}
	m.ascErr = managed.ErrASCNotApplied
	if _, err := l.SetWireguardServerObfuscation(context.Background(), "Wireguard3", map[string]any{"jc": float64(7)}); err == nil || !strings.Contains(err.Error(), "some may have applied") {
		t.Fatalf("err = %v", err)
	}
}
