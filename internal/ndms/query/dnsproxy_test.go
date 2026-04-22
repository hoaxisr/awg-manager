package query

import (
	"context"
	"errors"
	"testing"
	"time"
)

const dnsProxyPath = "/show/sc/dns-proxy/route"

const sampleDNSProxyJSON = `{
	"group1": {"interface": "Wireguard0", "auto": false, "reject": false},
	"group2": {"reject": true}
}`

func TestDNSProxyStore_List_OS5_ParsesAndCaches(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(dnsProxyPath, sampleDNSProxyJSON)
	s := NewDNSProxyStore(fg, NopLogger(), func() bool { return true })

	got, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len: want 2, got %d", len(got))
	}
	_, _ = s.List(context.Background())
	if fg.Calls(dnsProxyPath) != 1 {
		t.Errorf("calls: %d", fg.Calls(dnsProxyPath))
	}
}

func TestDNSProxyStore_List_OS4_ReturnsErrNotSupported(t *testing.T) {
	fg := newFakeGetter()
	s := NewDNSProxyStore(fg, NopLogger(), func() bool { return false })

	_, err := s.List(context.Background())
	if !errors.Is(err, ErrNotSupportedOnOS4) {
		t.Errorf("err: want ErrNotSupportedOnOS4, got %v", err)
	}
	if fg.Calls(dnsProxyPath) != 0 {
		t.Errorf("calls: want 0 (no NDMS call on OS4), got %d", fg.Calls(dnsProxyPath))
	}
}

func TestDNSProxyStore_List_ServesStaleOnError(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(dnsProxyPath, sampleDNSProxyJSON)
	s := NewDNSProxyStoreWithTTL(fg, NopLogger(), func() bool { return true }, 20*time.Millisecond)
	_, _ = s.List(context.Background())
	time.Sleep(30 * time.Millisecond)
	fg.SetError(dnsProxyPath, errors.New("boom"))
	got, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("stale-ok: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("len: %d", len(got))
	}
}

func TestDNSProxyStore_List_EmptyArray(t *testing.T) {
	// NDMS returns `[]` instead of `{}` when no routes are configured.
	// This used to crash decode: "cannot unmarshal array into map[string]...".
	fg := newFakeGetter()
	fg.SetJSON(dnsProxyPath, `[]`)
	s := NewDNSProxyStore(fg, NopLogger(), func() bool { return true })

	got, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("empty array: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len: want 0, got %d", len(got))
	}
}

func TestDNSProxyStore_List_PopulatedArray(t *testing.T) {
	// Legacy NDMS shape: array of objects with an explicit group field.
	fg := newFakeGetter()
	fg.SetJSON(dnsProxyPath, `[
		{"group":"LIST_A","interface":"Wireguard0","auto":true,"reject":false},
		{"group":"LIST_B","interface":"","auto":false,"reject":true}
	]`)
	s := NewDNSProxyStore(fg, NopLogger(), func() bool { return true })

	got, err := s.List(context.Background())
	if err != nil {
		t.Fatalf("populated array: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len: want 2, got %d", len(got))
	}
	if got[0].Group != "LIST_A" || got[0].Interface != "Wireguard0" || !got[0].Auto {
		t.Errorf("row 0: %+v", got[0])
	}
	if got[1].Group != "LIST_B" || !got[1].Reject {
		t.Errorf("row 1: %+v", got[1])
	}
}

func TestDNSProxyStore_InvalidateAllForcesRefetch(t *testing.T) {
	fg := newFakeGetter()
	fg.SetJSON(dnsProxyPath, sampleDNSProxyJSON)
	s := NewDNSProxyStore(fg, NopLogger(), func() bool { return true })
	_, _ = s.List(context.Background())
	s.InvalidateAll()
	_, _ = s.List(context.Background())
	if fg.Calls(dnsProxyPath) != 2 {
		t.Errorf("calls: %d", fg.Calls(dnsProxyPath))
	}
}
