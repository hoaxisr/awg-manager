package ipinspect

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"testing"
	"time"
)

type fakeResolver struct {
	mu       sync.Mutex
	ptrs     map[string][]string
	ptrErrs  map[string]error
	txts     map[string][]string
	txtErrs  map[string]error
	ptrCalls []string
	txtCalls []string
}

func (f *fakeResolver) LookupAddr(_ context.Context, addr string) ([]string, error) {
	f.mu.Lock()
	f.ptrCalls = append(f.ptrCalls, addr)
	names, ok := f.ptrs[addr]
	err := f.ptrErrs[addr]
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("not found")
	}
	return names, nil
}

func (f *fakeResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	f.mu.Lock()
	f.txtCalls = append(f.txtCalls, name)
	recs, ok := f.txts[name]
	err := f.txtErrs[name]
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("not found")
	}
	return recs, nil
}

func TestClassifyAddr(t *testing.T) {
	cases := []struct {
		ip     string
		family string
		scope  string
	}{
		{"1.1.1.1", "ipv4", "public"},
		{"::ffff:8.8.8.8", "ipv4", "public"},
		{"2606:4700:4700::1111", "ipv6", "public"},
		{"64:ff9b::0808:0808", "nat64", "public"},
		{"64:ff9b:1::8.8.8.8", "nat64", "public"},
		{"198.18.0.5", "ipv4", "benchmark"},
		{"198.19.255.254", "ipv4", "benchmark"},
		{"192.168.1.10", "ipv4", "private"},
		{"10.0.0.1", "ipv4", "private"},
		{"fd00::1", "ipv6", "private"},
		{"127.0.0.1", "ipv4", "loopback"},
		{"::1", "ipv6", "loopback"},
		{"169.254.1.2", "ipv4", "link-local"},
		{"fe80::1", "ipv6", "link-local"},
		{"100.64.10.1", "ipv4", "cgnat"},
		{"224.0.0.1", "ipv4", "multicast"},
		{"ff02::1", "ipv6", "multicast"},
		{"203.0.113.5", "ipv4", "reserved"},
		{"2001:db8::1", "ipv6", "reserved"},
		{"0.0.0.0", "ipv4", "reserved"},
	}

	for _, tc := range cases {
		addr := netip.MustParseAddr(tc.ip)
		fam, scope := ClassifyAddr(addr)
		if fam != tc.family || scope != tc.scope {
			t.Errorf("ClassifyAddr(%s) = (%q, %q), want (%q, %q)", tc.ip, fam, scope, tc.family, tc.scope)
		}
	}
}

func TestCymruOriginQuery(t *testing.T) {
	if got := CymruOriginQuery(netip.MustParseAddr("1.2.3.4")); got != "4.3.2.1.origin.asn.cymru.com" {
		t.Fatalf("IPv4 origin query = %q", got)
	}
	v6 := CymruOriginQuery(netip.MustParseAddr("2606:4700:4700::1111"))
	want := "1.1.1.1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.7.4.0.0.7.4.6.0.6.2.origin6.asn.cymru.com"
	if v6 != want {
		t.Fatalf("IPv6 origin query = %q, want %q", v6, want)
	}
}

func TestParseCymruRecords(t *testing.T) {
	asn, prefix, cc, reg, ok := ParseCymruOriginTXT("13335 13336 | 1.1.1.0/24 | au | apnic | 2011-08-11")
	if !ok || asn != 13335 || prefix != "1.1.1.0/24" || cc != "AU" || reg != "apnic" {
		t.Fatalf("ParseCymruOriginTXT multi-origin = (%d, %q, %q, %q, %v)", asn, prefix, cc, reg, ok)
	}

	if _, _, _, _, ok := ParseCymruOriginTXT("bad record"); ok {
		t.Fatalf("expected invalid origin record to fail")
	}

	org, cc, reg := ParseCymruASNTXT("13335 | US | arin | 2010-07-14 | CLOUDFLARENET, US")
	if org != "CLOUDFLARENET, US" || cc != "US" || reg != "arin" {
		t.Fatalf("ParseCymruASNTXT = (%q, %q, %q)", org, cc, reg)
	}
}

func TestServiceInspect_PublicAndCache(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	resMock := &fakeResolver{
		ptrs: map[string][]string{
			"1.1.1.1": {"one.one.one.one.", "one.one.one.one."},
		},
		txts: map[string][]string{
			"1.1.1.1.origin.asn.cymru.com": {"13335 | 1.1.1.0/24 | AU | apnic | 2011-08-11"},
			"AS13335.asn.cymru.com":        {"13335 | US | arin | 2010-07-14 | CLOUDFLARENET, US"},
		},
	}

	svc := NewServiceWithResolver(resMock, func() time.Time { return now })
	r1, err := svc.Inspect(context.Background(), "1.1.1.1")
	if err != nil {
		t.Fatalf("Inspect error = %v", err)
	}
	if r1.Cached {
		t.Fatalf("first call must not be cached")
	}
	if r1.Family != "ipv4" || r1.Scope != "public" || r1.PTR != "one.one.one.one" || len(r1.PTRs) != 1 || r1.ASN != 13335 || r1.Prefix != "1.1.1.0/24" || r1.Organization != "CLOUDFLARENET, US" || r1.Country != "AU" || r1.Registry != "apnic" {
		t.Fatalf("unexpected result: %+v", r1)
	}

	r2, err := svc.Inspect(context.Background(), "::ffff:1.1.1.1")
	if err != nil {
		t.Fatalf("second Inspect error = %v", err)
	}
	if !r2.Cached || r2.IP != "1.1.1.1" {
		t.Fatalf("expected cached IPv4-mapped result, got %+v", r2)
	}
	if len(resMock.ptrCalls) != 1 || len(resMock.txtCalls) != 2 {
		t.Fatalf("expected cache hit to avoid DNS queries, ptrCalls=%d txtCalls=%d", len(resMock.ptrCalls), len(resMock.txtCalls))
	}

	// Expire entry and confirm fresh lookup reuses AS cache when within asTTL.
	now = now.Add(31 * time.Minute)
	r3, err := svc.Inspect(context.Background(), "1.1.1.1")
	if err != nil {
		t.Fatalf("third Inspect error = %v", err)
	}
	if r3.Cached || r3.Organization != "CLOUDFLARENET, US" {
		t.Fatalf("expected refreshed IP with AS cache hit, got %+v", r3)
	}
	if len(resMock.txtCalls) != 3 {
		t.Fatalf("expected origin TXT only (AS cached), got txtCalls=%v", resMock.txtCalls)
	}
}

func TestServiceInspect_IPv6Public(t *testing.T) {
	resMock := &fakeResolver{
		ptrs: map[string][]string{
			"2606:4700:4700::1111": {"one.one.one.one."},
		},
		txts: map[string][]string{
			"1.1.1.1.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.0.7.4.0.0.7.4.6.0.6.2.origin6.asn.cymru.com": {"13335 | 2606:4700:4700::/48 | US | arin | 2018-04-01"},
			"AS13335.asn.cymru.com": {"13335 | US | arin | 2010-07-14 | CLOUDFLARENET, US"},
		},
	}

	svc := NewServiceWithResolver(resMock, nil)
	res, err := svc.Inspect(context.Background(), "2606:4700:4700::1111")
	if err != nil {
		t.Fatalf("Inspect IPv6 error = %v", err)
	}
	if res.Family != "ipv6" || res.Scope != "public" || res.ASN != 13335 || res.Prefix != "2606:4700:4700::/48" {
		t.Fatalf("unexpected IPv6 result: %+v", res)
	}
}

func TestServiceInspect_ScopesAndErrors(t *testing.T) {
	resMock := &fakeResolver{
		ptrs: map[string][]string{
			"192.168.1.50": {"nas.lan."},
		},
	}
	svc := NewServiceWithResolver(resMock, nil)

	if _, err := svc.Inspect(context.Background(), "not-an-ip"); !errors.Is(err, ErrInvalidIP) {
		t.Fatalf("expected ErrInvalidIP, got %v", err)
	}

	// Benchmark/FakeIP: zero DNS queries.
	bench, err := svc.Inspect(context.Background(), "198.18.0.5")
	if err != nil {
		t.Fatalf("benchmark Inspect error = %v", err)
	}
	if bench.Scope != "benchmark" || len(resMock.ptrCalls) != 0 || len(resMock.txtCalls) != 0 {
		t.Fatalf("benchmark IP must not hit DNS: %+v, ptr=%v txt=%v", bench, resMock.ptrCalls, resMock.txtCalls)
	}

	// Private IP: PTR allowed, zero Cymru TXT calls.
	priv, err := svc.Inspect(context.Background(), "192.168.1.50")
	if err != nil {
		t.Fatalf("private Inspect error = %v", err)
	}
	if priv.Scope != "private" || priv.PTR != "nas.lan" || len(resMock.txtCalls) != 0 {
		t.Fatalf("private IP unexpected: %+v, txt=%v", priv, resMock.txtCalls)
	}
}

func TestServiceInspect_PartialFailureAndCacheEviction(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	resMock := &fakeResolver{
		ptrs: map[string][]string{},
		ptrErrs: map[string]error{
			"8.8.4.4": errors.New("nxdomain"),
		},
		txts: map[string][]string{
			"4.4.8.8.origin.asn.cymru.com": {"15169 | 8.8.4.0/24 | US | arin | 1992-12-01"},
		},
		txtErrs: map[string]error{
			"AS15169.asn.cymru.com": errors.New("timeout"),
		},
	}

	svc := NewServiceWithResolver(resMock, func() time.Time { return now })
	res, err := svc.Inspect(context.Background(), "8.8.4.4")
	if err != nil {
		t.Fatalf("partial lookup should not error, got %v", err)
	}
	if res.ASN != 15169 || res.Prefix != "8.8.4.0/24" || res.PTR != "" || res.Organization != "" {
		t.Fatalf("expected partial ASN metadata without org/PTR, got %+v", res)
	}

	// Fill cache past maxEntries to assert bounded size.
	for i := 0; i < defaultMaxEntries+20; i++ {
		ip := fmt.Sprintf("1.0.%d.%d", i/250, (i%250)+1)
		now = now.Add(time.Millisecond)
		if _, err := svc.Inspect(context.Background(), ip); err != nil {
			t.Fatalf("Inspect(%s) err = %v", ip, err)
		}
	}
	svc.mu.Lock()
	cacheLen := len(svc.ipCache)
	svc.mu.Unlock()
	if cacheLen > defaultMaxEntries {
		t.Fatalf("cache size %d exceeded maxEntries %d", cacheLen, defaultMaxEntries)
	}
}
