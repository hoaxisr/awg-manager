package router

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func dnsServerByTag(t *testing.T, svc *ServiceImpl, tag string) DNSServer {
	t.Helper()
	servers, err := svc.ListDNSServers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(servers, func(s DNSServer) bool { return s.Tag == tag })
	if i < 0 {
		t.Fatalf("%s missing: %#v", tag, servers)
	}
	return servers[i]
}

func outboundByTag(t *testing.T, svc *ServiceImpl, tag string) Outbound {
	t.Helper()
	list, err := svc.ListCompositeOutbounds(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(list, func(o CompositeOutboundView) bool { return o.Tag == tag })
	if i < 0 {
		t.Fatalf("%s missing: %#v", tag, list)
	}
	return list[i].Outbound
}

// SetDNSServerDetour ведёт себя как сохранение формы с другим detour: с
// detour адрес отправителя и bootstrap-резолвер уходят (sing-box их
// игнорирует, а адрес с detour validateDNSBindAddress отвергает), без него
// адрес отправителя подставляется заново. Прочие поля не трогаются.
func TestSetDNSServerDetour_FollowsTheForm(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	ctx := context.Background()
	local := []string{"192.168.0.1/24"}
	stubLocalAddrs(svc, &local)
	if err := svc.AddCompositeOutbound(ctx, Outbound{Type: "selector", Tag: "vpn", Outbounds: []string{"direct"}}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []DNSServer{
		{Tag: "boot", Type: "udp", Server: "1.1.1.1"},
		{Tag: "router", Type: "udp", Server: "192.168.0.1", Strategy: "ipv4_only"},
		{Tag: "doh", Type: "https", Server: "dns.google", Path: "/dns-query", DomainResolver: &DomainResolver{Server: "boot"}},
	} {
		if err := svc.AddDNSServer(ctx, s); err != nil {
			t.Fatalf("add %s: %v", s.Tag, err)
		}
	}
	if got := dnsServerByTag(t, svc, "router"); got.Inet4BindAddress != "192.168.0.1" {
		t.Fatalf("precondition: the router's own address gets a sender address, got %#v", got)
	}

	if err := svc.SetDNSServerDetour(ctx, "router", "vpn"); err != nil {
		t.Fatalf("set detour: %v", err)
	}
	got := dnsServerByTag(t, svc, "router")
	if got.Detour != "vpn" || got.Inet4BindAddress != "" {
		t.Fatalf("with a detour the sender address must go: %#v", got)
	}
	if got.Server != "192.168.0.1" || got.Strategy != "ipv4_only" {
		t.Fatalf("other fields must be kept: %#v", got)
	}
	if err := svc.SetDNSServerDetour(ctx, "doh", "vpn"); err != nil {
		t.Fatalf("set detour on doh: %v", err)
	}
	if got := dnsServerByTag(t, svc, "doh"); got.DomainResolver != nil || got.Path != "/dns-query" {
		t.Fatalf("with a detour the bootstrap resolver must go and the rest stay: %#v", got)
	}

	// "direct" is how the form spells "no detour".
	if err := svc.SetDNSServerDetour(ctx, "router", "direct"); err != nil {
		t.Fatalf("clear detour: %v", err)
	}
	got = dnsServerByTag(t, svc, "router")
	if got.Detour != "" || got.Inet4BindAddress != "192.168.0.1" {
		t.Fatalf("without a detour the sender address must come back: %#v", got)
	}
	if !svc.StagingStatus(ctx).HasDraft {
		t.Fatal("the edit must land in the draft")
	}
}

// Отказ ничего не пишет: сервер остаётся как был.
func TestSetDNSServerDetour_Refusals(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	ctx := context.Background()
	if err := svc.AddCompositeOutbound(ctx, Outbound{Type: "selector", Tag: "vpn", Outbounds: []string{"direct"}}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []DNSServer{
		{Tag: "plain", Type: "udp", Server: "1.1.1.1"},
		{Tag: ManagedDNSDirectTag, Type: "udp", Server: "77.88.8.8"},
		{Tag: "loc", Type: "local"},
	} {
		if err := svc.AddDNSServer(ctx, s); err != nil {
			t.Fatalf("add %s: %v", s.Tag, err)
		}
	}
	cases := []struct {
		tag, detour string
		want        error
	}{
		{"plain", "no-such-outbound", ErrDNSServerDetour},
		{ManagedDNSDirectTag, "vpn", ErrDNSServerDetour},
		{"loc", "vpn", ErrDNSServerDetour},
		{"absent", "vpn", ErrDNSServerNotFound},
	}
	for _, c := range cases {
		before, _ := svc.ListDNSServers(ctx)
		err := svc.SetDNSServerDetour(ctx, c.tag, c.detour)
		if !errors.Is(err, c.want) {
			t.Errorf("%s → %s: err = %v, want %v", c.tag, c.detour, err, c.want)
		}
		after, _ := svc.ListDNSServers(ctx)
		if !slices.EqualFunc(before, after, func(a, b DNSServer) bool { return a.Tag == b.Tag && a.Detour == b.Detour }) {
			t.Errorf("%s: a refusal must write nothing: %#v → %#v", c.tag, before, after)
		}
	}
}

// SetCompositeOutboundMembers меняет только состав: порядок сохраняется
// (fallback идёт по нему), настройки проверки остаются, default селектора
// переезжает, только когда его участник выпал.
func TestSetCompositeOutboundMembers(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	ctx := context.Background()
	for _, o := range []Outbound{
		{Type: "selector", Tag: "a", Outbounds: []string{"direct"}},
		{Type: "selector", Tag: "b", Outbounds: []string{"direct"}},
		{Type: "selector", Tag: "c", Outbounds: []string{"direct"}},
		{Type: "selector", Tag: "pick", Outbounds: []string{"a", "b"}, Default: "b"},
		{Type: "fallback", Tag: "fb", Outbounds: []string{"a", "b"}, URL: "https://www.gstatic.com/generate_204", Interval: "3m", InterruptExistConnections: true},
	} {
		if err := svc.AddCompositeOutbound(ctx, o); err != nil {
			t.Fatalf("add %s: %v", o.Tag, err)
		}
	}

	if err := svc.SetCompositeOutboundMembers(ctx, "fb", []string{"c", "b", "a"}); err != nil {
		t.Fatal(err)
	}
	fb := outboundByTag(t, svc, "fb")
	if !slices.Equal(fb.Outbounds, []string{"c", "b", "a"}) {
		t.Fatalf("members must keep the given order: %v", fb.Outbounds)
	}
	if fb.URL == "" || fb.Interval != "3m" || !fb.InterruptExistConnections {
		t.Fatalf("the group's other settings must be kept: %#v", fb)
	}

	if err := svc.SetCompositeOutboundMembers(ctx, "pick", []string{"c", "b"}); err != nil {
		t.Fatal(err)
	}
	if got := outboundByTag(t, svc, "pick"); got.Default != "b" {
		t.Fatalf("a default still among the members must stay: %#v", got)
	}
	if err := svc.SetCompositeOutboundMembers(ctx, "pick", []string{"a", "c"}); err != nil {
		t.Fatal(err)
	}
	if got := outboundByTag(t, svc, "pick"); got.Default != "a" {
		t.Fatalf("a default that left the group must move to the first member: %#v", got)
	}
}

func TestSetCompositeOutboundMembers_Refusals(t *testing.T) {
	svc, _ := newOrchedTestService(t)
	ctx := context.Background()
	for _, o := range []Outbound{
		{Type: "selector", Tag: "a", Outbounds: []string{"direct"}},
		{Type: "urltest", Tag: "auto", Outbounds: []string{"a"}},
	} {
		if err := svc.AddCompositeOutbound(ctx, o); err != nil {
			t.Fatalf("add %s: %v", o.Tag, err)
		}
	}
	cases := []struct {
		name    string
		tag     string
		members []string
		want    error
	}{
		{"unknown member", "auto", []string{"a", "ghost"}, ErrCompositeMemberUnknown},
		{"no members", "auto", nil, ErrOutboundInvalid},
		{"direct in urltest", "auto", []string{"direct"}, ErrOutboundInvalid},
		{"itself", "auto", []string{"auto"}, ErrOutboundInvalid},
		{"cycle", "a", []string{"auto"}, nil},
		{"unknown group", "nope", []string{"a"}, ErrOutboundNotFound},
		{"not a group", "lan", []string{"a"}, ErrOutboundInvalid},
	}
	if err := svc.withConfig(ctx, "outbounds", func(c *RouterConfig) error {
		c.Outbounds = append(c.Outbounds, Outbound{Type: "direct", Tag: "lan"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		err := svc.SetCompositeOutboundMembers(ctx, c.tag, c.members)
		if err == nil {
			t.Errorf("%s: must be refused", c.name)
			continue
		}
		if c.want != nil && !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
	if got := outboundByTag(t, svc, "auto"); !slices.Equal(got.Outbounds, []string{"a"}) {
		t.Fatalf("a refusal must write nothing: %v", got.Outbounds)
	}
	if got := outboundByTag(t, svc, "a"); !slices.Equal(got.Outbounds, []string{"direct"}) {
		t.Fatalf("a refused cycle must write nothing: %v", got.Outbounds)
	}
	if err := svc.SetCompositeOutboundMembers(ctx, "lan", []string{"a"}); err == nil || !strings.Contains(err.Error(), "не группа") {
		t.Fatalf("an outbound that is not a group is refused as such, not by a later check: %v", err)
	}
	if got := outboundByTag(t, svc, "lan"); len(got.Outbounds) != 0 {
		t.Fatalf("an outbound that is not a group gets no members: %v", got.Outbounds)
	}
}
