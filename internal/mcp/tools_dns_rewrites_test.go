package mcp_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func rewriteAt(t *testing.T, s *sdk.ClientSession, index int) map[string]any {
	t.Helper()
	res, out := callTool(t, s, "list_singbox_dns_rewrites", nil)
	if res.IsError {
		t.Fatal(toolText(res))
	}
	r := out["rewrites"].([]any)[index].(map[string]any)
	if len(r["revision"].(string)) != 64 || r["index"] != float64(index) {
		t.Fatalf("a listed rewrite needs its position and revision: %v", r)
	}
	return r
}

func TestTools_DNSRewriteCRUD(t *testing.T) {
	s, fake := newTestSession(t)
	fake.RouterStatus.RoutingMode = "fakeip-tun"
	fake.Draft = append([]mcpsrv.SingboxRule(nil), fake.Rules...)
	draft := append([]mcpsrv.SingboxRule(nil), fake.Draft...)
	res, out := callTool(t, s, "add_singbox_dns_rewrite", map[string]any{
		"pattern": "NAS.Example.Invalid.", "ips": []string{"192.0.2.8", "2001:db8::8"},
	})
	if res.IsError || out["saved"] != true || out["stateKnown"] != true || !strings.Contains(toolText(res), "not staged") {
		t.Fatalf("an immediate save must say so: %v %s", out, toolText(res))
	}
	r := rewriteAt(t, s, 2)
	if r["pattern"] != "nas.example.invalid" || !reflect.DeepEqual(r["ips"], []any{"192.0.2.8", "2001:db8::8"}) {
		t.Fatalf("saved rewrite = %v", r)
	}
	args := map[string]any{"index": 2, "expectRevision": r["revision"], "pattern": "*.example.invalid", "ips": []string{"192.0.2.9"}}
	if res, _ := callTool(t, s, "update_singbox_dns_rewrite", args); res.IsError {
		t.Fatal(toolText(res))
	}
	updated := rewriteAt(t, s, 2)
	if updated["pattern"] != "*.example.invalid" || updated["revision"] == r["revision"] {
		t.Fatalf("update = %v", updated)
	}
	if res, _ := callTool(t, s, "delete_singbox_dns_rewrite", map[string]any{"index": 2, "expectRevision": r["revision"]}); !res.IsError {
		t.Fatal("an outdated revision must not delete the updated entry")
	}
	res, out = callTool(t, s, "delete_singbox_dns_rewrite", map[string]any{"index": 2, "expectRevision": updated["revision"]})
	if res.IsError || out["saved"] != true || len(out["rewrites"].([]any)) != 2 {
		t.Fatalf("delete: %v %s", out, toolText(res))
	}
	if !reflect.DeepEqual(fake.Draft, draft) {
		t.Fatal("DNS Rewrite CRUD touched the shared router draft")
	}
}

type dnsRewriteSpy struct {
	mcpsrv.Deps
	calls int
	out   mcpsrv.SingboxDNSRewriteResult
}

func (d *dnsRewriteSpy) AddSingboxDNSRewrite(context.Context, mcpsrv.SingboxDNSRewriteInput) (mcpsrv.SingboxDNSRewriteResult, error) {
	d.calls++
	return d.out, nil
}
func (d *dnsRewriteSpy) UpdateSingboxDNSRewrite(context.Context, int, string, mcpsrv.SingboxDNSRewriteInput) (mcpsrv.SingboxDNSRewriteResult, error) {
	d.calls++
	return d.out, nil
}
func (d *dnsRewriteSpy) DeleteSingboxDNSRewrite(context.Context, int, string) (mcpsrv.SingboxDNSRewriteResult, error) {
	d.calls++
	return d.out, nil
}

func TestTools_DNSRewriteValidationBeforeDeps(t *testing.T) {
	d := &dnsRewriteSpy{}
	s := connect(t, mcpsrv.NewServer(d, "test"))
	for _, pattern := range []string{"", "*", "x**.invalid", ".invalid", "x..invalid", "https://node.invalid/path", "a.invalid\nIN A 192.0.2.1", "a invalid", "-a.invalid", "K.invalid", "принтер.invalid", strings.Repeat("a", 64) + ".invalid", strings.Repeat("a.", 128)} {
		if res, _ := callTool(t, s, "add_singbox_dns_rewrite", map[string]any{"pattern": pattern, "ips": []string{"192.0.2.1"}}); !res.IsError {
			t.Errorf("accepted pattern %q", pattern)
		}
		if res, _ := callTool(t, s, "update_singbox_dns_rewrite", map[string]any{"index": 0, "expectRevision": strings.Repeat("0", 64), "pattern": pattern, "ips": []string{"192.0.2.1"}}); !res.IsError {
			t.Errorf("update accepted pattern %q", pattern)
		}
	}
	for _, ips := range [][]string{nil, {}, {""}, {"192.0.2.0/24"}, {"192.0.2.1:53"}, {"fe80::1%eth0"}, {"not-an-ip"}, make([]string, 65)} {
		if res, _ := callTool(t, s, "add_singbox_dns_rewrite", map[string]any{"pattern": "nas.invalid", "ips": ips}); !res.IsError {
			t.Errorf("accepted IPs %v", ips)
		}
	}
	for _, name := range []string{"update_singbox_dns_rewrite", "delete_singbox_dns_rewrite"} {
		for _, revision := range []string{"", "abc", strings.Repeat("z", 64)} {
			args := map[string]any{"index": 0, "expectRevision": revision, "pattern": "nas.invalid", "ips": []string{"192.0.2.1"}}
			if res, _ := callTool(t, s, name, args); !res.IsError {
				t.Errorf("%s accepted invalid revision", name)
			}
		}
		if res, _ := callTool(t, s, name, map[string]any{"index": -1, "expectRevision": strings.Repeat("0", 64), "pattern": "nas.invalid", "ips": []string{"192.0.2.1"}}); !res.IsError {
			t.Errorf("%s accepted negative index", name)
		}
	}
	if d.calls != 0 {
		t.Fatalf("%d invalid calls reached the dependencies", d.calls)
	}
}

func TestDNSRewriteInputBoundaries(t *testing.T) {
	domain := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
	for _, pattern := range []string{domain, "*.EXAMPLE.INVALID.", "node*.example.invalid", "xn--e1afmkfd.invalid"} {
		ips := make([]string, 64)
		for i := range ips {
			ips[i] = "::ffff:192.0.2.1"
		}
		got, err := mcpsrv.NormalizeSingboxDNSRewrite(mcpsrv.SingboxDNSRewriteInput{Pattern: pattern, IPs: ips})
		if err != nil || len(got.IPs) != 64 || got.IPs[0] != "192.0.2.1" || got.Pattern != strings.TrimSuffix(strings.ToLower(pattern), ".") {
			t.Fatalf("valid boundary: %+v %v", got, err)
		}
	}
}

func TestTools_DNSRewriteSavedButUnknown(t *testing.T) {
	d := &dnsRewriteSpy{out: mcpsrv.SingboxDNSRewriteResult{Saved: true, StateKnown: false}}
	s := connect(t, mcpsrv.NewServer(d, "test"))
	res, out := callTool(t, s, "add_singbox_dns_rewrite", map[string]any{"pattern": "nas.invalid", "ips": []string{"192.0.2.1"}})
	if res.IsError || out["saved"] != true || out["stateKnown"] != false || out["rewrites"] == nil || !strings.Contains(toolText(res), "before retrying") {
		t.Fatalf("must distinguish an unreadable saved list from no entries: %v %s", out, toolText(res))
	}
}

func TestTools_DNSRewriteManagedAndScope(t *testing.T) {
	s, _ := newTestSession(t)
	r := rewriteAt(t, s, 1)
	for _, name := range []string{"update_singbox_dns_rewrite", "delete_singbox_dns_rewrite"} {
		args := map[string]any{"index": 1, "expectRevision": r["revision"]}
		if strings.HasPrefix(name, "update") {
			args["pattern"] = "test.invalid"
			args["ips"] = []string{"192.0.2.1"}
		}
		if res, _ := callTool(t, s, name, args); !res.IsError {
			t.Fatalf("%s edited a preset", name)
		}
	}
	ro := scopedSession(t, true)
	r = rewriteAt(t, ro, 0)
	for name, args := range map[string]map[string]any{
		"add_singbox_dns_rewrite":    {"pattern": "test.invalid", "ips": []string{"192.0.2.1"}},
		"update_singbox_dns_rewrite": {"index": 0, "expectRevision": r["revision"], "pattern": "test.invalid", "ips": []string{"192.0.2.1"}},
		"delete_singbox_dns_rewrite": {"index": 0, "expectRevision": r["revision"]},
	} {
		res, _ := callTool(t, ro, name, args)
		if !res.IsError || !strings.Contains(toolText(res), "read-only") {
			t.Fatalf("%s: %s", name, toolText(res))
		}
	}
	if got := rewriteAt(t, ro, 0); !reflect.DeepEqual(got, r) {
		t.Fatalf("read-only call changed state: %v", got)
	}
	tools, err := s.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "delete_singbox_dns_rewrite" && (tool.Annotations.ReadOnlyHint || tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint || tool.Annotations.IdempotentHint) {
			t.Fatalf("delete annotations = %+v", tool.Annotations)
		}
	}
}
