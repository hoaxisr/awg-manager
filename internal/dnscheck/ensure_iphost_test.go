package dnscheck

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/sys/netif"
)

type fakeNDMS struct {
	postResp json.RawMessage
	postErr  error

	postedPayloads []any
}

func (f *fakeNDMS) Post(_ context.Context, payload any) (json.RawMessage, error) {
	f.postedPayloads = append(f.postedPayloads, payload)
	return f.postResp, f.postErr
}

// fakeIPHost is a tiny stand-in for query.IPHostStore.
type fakeIPHost struct {
	entries        map[string]string
	invalidations  int
	overrideLookup func(domain string) (string, bool)
}

func (f *fakeIPHost) Lookup(_ context.Context, domain string) (string, bool) {
	if f.overrideLookup != nil {
		return f.overrideLookup(domain)
	}
	addr, ok := f.entries[domain]
	return addr, ok
}
func (f *fakeIPHost) Invalidate() { f.invalidations++ }

func TestLookupIPHost_Found(t *testing.T) {
	svc := &Service{ipHost: &fakeIPHost{entries: map[string]string{
		probeDomain: "192.168.1.1",
	}}}
	addr, ok := svc.lookupIPHost(context.Background(), probeDomain)
	if !ok || addr != "192.168.1.1" {
		t.Errorf("got (%q,%v), want (192.168.1.1,true)", addr, ok)
	}
}

func TestLookupIPHost_OtherDomainsPresent(t *testing.T) {
	svc := &Service{ipHost: &fakeIPHost{entries: map[string]string{
		"other.example": "10.0.0.1",
		probeDomain:     "192.168.1.1",
	}}}
	addr, ok := svc.lookupIPHost(context.Background(), probeDomain)
	if !ok || addr != "192.168.1.1" {
		t.Errorf("got (%q,%v), want (192.168.1.1,true)", addr, ok)
	}
}

func TestLookupIPHost_Missing(t *testing.T) {
	svc := &Service{ipHost: &fakeIPHost{entries: map[string]string{}}}
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
	svc := &Service{ndms: fake, ipHost: &fakeIPHost{}}

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

// On a successful write the cache MUST be invalidated so subsequent
// lookupIPHost calls observe the new entry instead of stale data.
func TestCreateIPHost_InvalidatesCacheOnSuccess(t *testing.T) {
	fake := &fakeNDMS{}
	ip := &fakeIPHost{}
	svc := &Service{ndms: fake, ipHost: ip}

	if err := svc.createIPHost(context.Background(), "awgm-dnscheck.test", "192.168.1.1"); err != nil {
		t.Fatalf("createIPHost: %v", err)
	}
	if ip.invalidations != 1 {
		t.Errorf("expected 1 cache invalidation after successful POST, got %d", ip.invalidations)
	}
}

// stubFirstIPv4 подменяет шов над чтением адреса br0 на время теста.
func stubFirstIPv4(t *testing.T, fn func(string) string) {
	t.Helper()
	old := firstIPv4
	firstIPv4 = fn
	t.Cleanup(func() { firstIPv4 = old })
}

// Дефолт шва — netif.FirstIPv4: иначе адрес роутера в записи ip host перестал
// бы браться у интерфейса.
func TestFirstIPv4Default_IsNetifFirstIPv4(t *testing.T) {
	if reflect.ValueOf(firstIPv4).Pointer() != reflect.ValueOf(netif.FirstIPv4).Pointer() {
		t.Fatal("firstIPv4 по умолчанию обязан быть netif.FirstIPv4")
	}
}

// Regression for the router-log spam: when the entry already matches, we
// must NOT issue a create POST — that's what triggered NDMS to log
// 'Core::Configurator: not found: "ip/host/awgm-dnscheck.test"'.
func TestEnsureIPHost_SkipsPostWhenAlreadyCorrect(t *testing.T) {
	stubFirstIPv4(t, func(iface string) string {
		if iface != "br0" {
			t.Fatalf("адрес читали у %q, а запись строится по br0", iface)
		}
		return "192.168.7.1"
	})
	fake := &fakeNDMS{}
	ip := &fakeIPHost{entries: map[string]string{probeDomain: "192.168.7.1"}}
	svc := &Service{ndms: fake, ipHost: ip}

	svc.EnsureIPHost(context.Background())

	if len(fake.postedPayloads) != 0 {
		t.Fatalf("совпадающая запись — POST'а быть не должно, got %d", len(fake.postedPayloads))
	}
}

// Контроль к предыдущему: расхождение адреса обязано вылиться ровно в один POST.
func TestEnsureIPHost_PostsWhenAddressDiffers(t *testing.T) {
	stubFirstIPv4(t, func(string) string { return "192.168.7.1" })
	fake := &fakeNDMS{}
	ip := &fakeIPHost{entries: map[string]string{probeDomain: "192.168.7.2"}}
	svc := &Service{ndms: fake, ipHost: ip}

	svc.EnsureIPHost(context.Background())

	if len(fake.postedPayloads) != 1 {
		t.Fatalf("устаревшая запись — ожидали ровно 1 POST, got %d", len(fake.postedPayloads))
	}
}
