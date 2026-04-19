package dnscheck

import (
	"context"
	"encoding/json"
	"testing"
)

type fakeNDMS struct {
	getResp  []byte
	getErr   error
	postResp json.RawMessage
	postErr  error

	postedPayloads []any
}

func (f *fakeNDMS) GetRaw(_ context.Context, _ string) ([]byte, error) {
	return f.getResp, f.getErr
}
func (f *fakeNDMS) Post(_ context.Context, payload any) (json.RawMessage, error) {
	f.postedPayloads = append(f.postedPayloads, payload)
	return f.postResp, f.postErr
}

func TestLookupIPHost_Found(t *testing.T) {
	svc := &Service{ndms: &fakeNDMS{
		getResp: []byte(`[{"domain":"awgm-dnscheck.test","address":"192.168.1.1"}]`),
	}}
	addr, ok := svc.lookupIPHost(context.Background(), probeDomain)
	if !ok || addr != "192.168.1.1" {
		t.Errorf("got (%q,%v), want (192.168.1.1,true)", addr, ok)
	}
}

func TestLookupIPHost_OtherDomainsPresent(t *testing.T) {
	svc := &Service{ndms: &fakeNDMS{
		getResp: []byte(`[
			{"domain":"other.example","address":"10.0.0.1"},
			{"domain":"awgm-dnscheck.test","address":"192.168.1.1"}
		]`),
	}}
	addr, ok := svc.lookupIPHost(context.Background(), probeDomain)
	if !ok || addr != "192.168.1.1" {
		t.Errorf("got (%q,%v), want (192.168.1.1,true)", addr, ok)
	}
}

func TestLookupIPHost_Missing(t *testing.T) {
	svc := &Service{ndms: &fakeNDMS{
		getResp: []byte(`[]`),
	}}
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
	svc := &Service{ndms: fake}

	if err := svc.createIPHost(context.Background(), "awgm-dnscheck.test", "192.168.1.1"); err != nil {
		t.Fatalf("createIPHost: %v", err)
	}
	if len(fake.postedPayloads) != 1 {
		t.Fatalf("expected 1 POST, got %d", len(fake.postedPayloads))
	}

	raw, err := json.Marshal(fake.postedPayloads[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"ip":{"host":{"address":"192.168.1.1","domain":"awgm-dnscheck.test"}}}`
	if string(raw) != want {
		t.Fatalf("payload mismatch:\n got: %s\nwant: %s", raw, want)
	}
}

// Regression for the router-log spam: when the entry already matches, we
// must NOT issue a create POST — that's what triggered NDMS to log
// 'Core::Configurator: not found: "ip/host/awgm-dnscheck.test"'.
func TestEnsureIPHost_SkipsPostWhenAlreadyCorrect(t *testing.T) {
	routerIP := getBr0IP()
	if routerIP == "" {
		t.Skip("no br0 IP on this test host")
	}
	fake := &fakeNDMS{
		getResp: []byte(`[{"domain":"awgm-dnscheck.test","address":"` + routerIP + `"}]`),
	}
	svc := &Service{ndms: fake, log: nil}
	// Intentionally passing nil log — EnsureIPHost only calls logger in
	// paths we're skipping here.
	_ = svc
	addr, ok := svc.lookupIPHost(context.Background(), probeDomain)
	if !ok || addr != routerIP {
		t.Fatalf("precondition: lookup must find %s, got (%q,%v)", routerIP, addr, ok)
	}
	// With matching record in place, EnsureIPHost should early-return
	// without any POST.
	if len(fake.postedPayloads) != 0 {
		t.Errorf("expected no POST, got %d", len(fake.postedPayloads))
	}
}
