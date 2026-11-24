package firc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func boolPtr(v bool) *bool { return &v }

const testDoHServer = "https://9.9.9.9/dns-query"

func dnsOnlyGroup(id string) Group {
	return Group{ID: id, Name: id, Enable: true, Interface: GroupInterface{DNSOnlyInterface},
		Resolve: GroupResolve{Server: testDoHServer}}
}

func newGroupsOperator(t *testing.T) *Operator {
	t.Helper()
	dir := t.TempDir()
	return NewOperator(Options{ConfigDir: dir, SocketPath: filepath.Join(dir, "firc.sock")})
}

// Случаи — из test_resolver_addr.c и test_yamlio.c демона.
func TestValidateResolveServer(t *testing.T) {
	pool := []netip.Prefix{netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("fd00:1234::/48")}
	ok := []string{
		"", "1.1.1.1", "1.1.1.1:5353", "2620:fe::fe", "[2620:fe::fe]", "[2620:fe::fe]:853",
		"https://1.1.1.1/", "HTTPS://dns.example:8443/dns-query", "https://[2620:fe::fe]/",
		"https://dns.comss.one/dns-query ", "https://a.example/d, 1.1.1.1 ,https://a.example/D",
		"https://a.example/d,https://b.example/d,https://c.example/d,9.9.9.9",
		"https://128.0.0.1/q, https://192.168.1.1/q, https://[::2]/q",
		"1.1.1.1,1.1.1.1:54", "https://198.20.0.1/q", "https://[fd00:1235::53]/q",
		"https://h.example/" + strings.Repeat("p", 255-len("https://h.example/")),
	}
	for _, s := range ok {
		if err := validateResolveServer(s, pool); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	bad := map[string]string{
		" ":        "empty entry",
		"1.1.1.1,": "empty entry",
		"1.1.1.1,2.2.2.2,3.3.3.3,4.4.4.4,5.5.5.5":           "more than 4",
		"1.1.1.1:53, 1.1.1.1":                               "twice",
		"https://a.example/d, 1.1.1.1, https://a.example/d": "twice",
		"dns.example":                        "not an address literal",
		"1.1.1.1:":                           "not an address literal",
		"[1.1.1.1]":                          "not an address literal",
		"fe80::1%eth0":                       "not an address literal",
		"1.1.1.1:0":                          "port",
		"1.1.1.1:65536":                      "port",
		"1.1.1.1:000053":                     "port",
		"127.0.0.1":                          "sink",
		"0.0.0.0":                            "sink",
		"::1":                                "sink",
		"::":                                 "sink",
		"::ffff:9.9.9.9":                     "IPv4-mapped",
		"198.19.0.5":                         "address pool",
		"[fd00:1234::53]:53":                 "address pool",
		"http://dns.example/dns-query":       "not a DoH URL",
		"https:///dns-query":                 "not a DoH URL",
		"https://:443/dns-query":             "not a DoH URL",
		"https://dns.example":                "not a DoH URL",
		"https://dns.example:443":            "not a DoH URL",
		"https://user@dns.example/dns-query": "not a DoH URL",
		"https://dns.example/dns-query?x=1":  "not a DoH URL",
		"https://dns.example/dns-query#f":    "not a DoH URL",
		"https://dns.example/dns query":      "not a DoH URL",
		"https://dns.example:/x":             "not a DoH URL",
		"https://dns.example:0/x":            "not a DoH URL",
		"https://dns.example:65536/x":        "not a DoH URL",
		"https://dns.example:44x/x":          "not a DoH URL",
		"https://2620:fe::fe/x":              "not a DoH URL",
		"https://[2606:4700::1111/x":         "not a DoH URL",
		"https://[]/x":                       "not a DoH URL",
		"https://[abc]/x":                    "not a DoH URL",
		"https://[1.1.1.1]/x":                "not a DoH URL",
		"https://h.example/" + strings.Repeat("p", 256-len("https://h.example/")): "not a DoH URL",
		"https://127.9.9.9:8443/q":                        "sink",
		"https://0.0.0.0/q":                               "sink",
		"https://[::1]/q":                                 "sink",
		"https://[::ffff:9.9.9.9]/q":                      "IPv4-mapped",
		"https://dns.example/q,https://198.18.0.9:8443/q": "address pool",
		"https://[fd00:1234::53]/q":                       "address pool",
	}
	for s, want := range bad {
		if err := validateResolveServer(s, pool); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err=%v, want %q", s, err, want)
		}
	}
}

func TestValidateGroups_DNSOnly(t *testing.T) {
	with := func(mut func(*Group)) Group {
		g := dnsOnlyGroup("d")
		mut(&g)
		return g
	}
	for _, g := range []Group{
		dnsOnlyGroup("d"),
		with(func(g *Group) { g.Resolve.Fallback = boolPtr(false) }),
		with(func(g *Group) { g.Rules = []Rule{{Type: "namespace", Rule: "example.com"}} }),
		{Interface: GroupInterface{"nwg0"}, Cloudflare: true, Resolve: GroupResolve{Fallback: boolPtr(true)}, Rules: []Rule{{Type: "subnet", Rule: "1.0.0.0/8"}}},
	} {
		if err := ValidateGroups([]Group{g}, nil); err != nil {
			t.Errorf("%+v: %v", g, err)
		}
	}
	for field, g := range map[string]Group{
		"resolve.server":   with(func(g *Group) { g.Resolve.Server = "" }),
		"cloudflare":       with(func(g *Group) { g.Cloudflare = true }),
		"rules":            with(func(g *Group) { g.Rules = []Rule{{Type: "subnet6", Rule: "2000::/3"}} }),
		"interface":        with(func(g *Group) { g.Interface = GroupInterface{"nwg0", DNSOnlyInterface} }),
		"resolve.fallback": {Interface: GroupInterface{"nwg0"}, Resolve: GroupResolve{Fallback: boolPtr(false)}},
	} {
		var invalid *GroupValidationError
		if err := ValidateGroups([]Group{g}, nil); !errors.As(err, &invalid) || invalid.Field != field {
			t.Errorf("%s: err=%v", field, err)
		}
	}
}

// Пул берётся из настроек демона, а без них — умолчание v4 и сгенерированный префикс v6.
func TestSaveGroupsFile_RefusesResolverInsidePool(t *testing.T) {
	op := newGroupsOperator(t)
	save := func(server string) error {
		return op.SaveGroupsFile([]Group{{ID: "g", Name: "g", Interface: GroupInterface{"nwg0"}, Resolve: GroupResolve{Server: server}}})
	}
	if save("198.18.0.1") == nil {
		t.Error("the default pool was not checked")
	}
	if err := os.WriteFile(filepath.Join(op.ConfigDir(), "v6-pool-prefix"), []byte("fd12:3456:789a::/48\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if save("https://[fd12:3456:789a::1]/q") == nil {
		t.Error("the generated v6 prefix was not checked")
	}
	conf := "app:\n  addressPool:\n    v4:\n      pool: 10.64.0.0/16\n    v6:\n      pool: fd99::/48\n"
	if err := os.WriteFile(op.ConfigPath(), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"10.64.1.1", "https://[fd99::1]/q"} {
		if save(s) == nil {
			t.Errorf("%s: the configured pool was not checked", s)
		}
	}
	if err := save("198.18.0.1"); err != nil {
		t.Errorf("198.18.0.1 is outside the configured pool: %v", err)
	}
}

// Невалидное не пишется офлайн: файл остаётся прежним.
func TestStageGroupsOffline_RefusesInvalidGroup(t *testing.T) {
	op := newGroupsOperator(t)
	if _, err := op.StageGroupsOffline([]Group{dnsOnlyGroup("d1")}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(op.GroupsPath())
	bad := dnsOnlyGroup("d1")
	bad.Resolve.Server = "https://dns.example"
	var invalid *GroupValidationError
	if _, err := op.StageGroupsOffline([]Group{bad}); !errors.As(err, &invalid) {
		t.Fatalf("err=%v", err)
	}
	if after, _ := os.ReadFile(op.GroupsPath()); string(after) != string(before) {
		t.Fatalf("groups.yaml changed:\n%s", after)
	}
}

func TestGroupsFile_KeepsDoHAndFallback(t *testing.T) {
	op := newGroupsOperator(t)
	yamlIn := `configVersion: 0.7.0
groups:
  - id: "00000001"
    name: DoH
    interface: dns
    enable: true
    resolve:
      server: https://9.9.9.9/dns-query, 1.1.1.1
      fallback: false
  - id: "00000002"
    name: DoH2
    interface: dns
    enable: true
    resolve:
      server: https://dns.example/dns-query
  - id: "00000003"
    name: VPN
    interface: nwg0
    enable: true
    resolve:
      tunnel: true
      server: https://1.1.1.1/dns-query
`
	if err := os.WriteFile(op.GroupsPath(), []byte(yamlIn), 0o644); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		grps, err := op.LoadGroupsFile()
		if err != nil || len(grps) != 3 {
			t.Fatalf("load: %+v %v", grps, err)
		}
		if err := op.SaveGroupsFile(grps); err != nil {
			t.Fatal(err)
		}
	}
	out, _ := os.ReadFile(op.GroupsPath())
	flat := strings.Join(strings.Fields(string(out)), " ")
	for _, want := range []string{
		"resolve: server: https://9.9.9.9/dns-query, 1.1.1.1 fallback: false",
		"resolve: server: https://dns.example/dns-query",
		"resolve: tunnel: true server: https://1.1.1.1/dns-query",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("lost %q in:\n%s", want, out)
		}
	}
	if n := strings.Count(string(out), "tunnel:"); n != 1 {
		t.Errorf("tunnel written %d times, a dns group has none:\n%s", n, out)
	}
	if n := strings.Count(string(out), "fallback:"); n != 1 {
		t.Errorf("fallback written %d times, only off is written:\n%s", n, out)
	}
}

// Запрос без tunnel у обычной группы значит то же, что и раньше: выключено явно.
// Без resolve демон считает tunnel включённым (config/models.c): прочитав такой файл, Go не выключает его.
func TestGroupsFile_TunnelExplicitForOrdinaryGroups(t *testing.T) {
	op := newGroupsOperator(t)
	if err := op.SaveGroupsFile([]Group{{ID: "g", Name: "g", Interface: GroupInterface{"nwg0"}}}); err != nil {
		t.Fatal(err)
	}
	if out, _ := os.ReadFile(op.GroupsPath()); !strings.Contains(string(out), "tunnel: true") {
		t.Fatalf("tunnel not written:\n%s", out)
	}
	if err := os.WriteFile(op.GroupsPath(), []byte("groups:\n  - id: g\n    name: g\n    interface: nwg0\n  - id: o\n    name: o\n    interface: nwg0\n    resolve:\n      tunnel: false\n  - id: d\n    name: d\n    interface: dns\n    resolve:\n      server: 9.9.9.9\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	grps, err := op.LoadGroupsFile()
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal([]GroupResolve{grps[0].Resolve, grps[1].Resolve, grps[2].Resolve})
	if want := `[{"tunnel":true},{"tunnel":false},{"server":"9.9.9.9","fallback":true}]`; string(got) != want {
		t.Fatalf("loaded resolve=%s want %s", got, want)
	}
	if err := op.SaveGroupsFile(grps); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(op.GroupsPath())
	if s := string(out); strings.Count(s, "tunnel: true") != 1 || strings.Count(s, "tunnel: false") != 1 || strings.Count(s, "tunnel:") != 2 {
		t.Fatalf("round trip changed tunnel:\n%s", s)
	}
}

func TestPutGroups_DNSOnlyPayload(t *testing.T) {
	mockSrv, handler := newMockFircHandler()
	srv := httptest.NewServer(handler)
	defer srv.Close()
	g := dnsOnlyGroup("d1")
	g.Resolve.Tunnel = boolPtr(false)
	if _, err := NewHTTPClient(srv.URL, srv.Client()).PutGroups(context.Background(), []Group{g}, true); err != nil {
		t.Fatal(err)
	}
	mockSrv.mu.Lock()
	defer mockSrv.mu.Unlock()
	raw, _ := json.Marshal(mockSrv.groups[0].Resolve)
	if want := `{"server":"https://9.9.9.9/dns-query","fallback":true}`; string(raw) != want {
		t.Fatalf("resolve=%s want %s", raw, want)
	}
}

// fallback без значения в запросе не сбрасывает сохранённый — и офлайн, и при живом демоне.
func TestApplyGroups_KeepsOmittedFallback(t *testing.T) {
	check := func(t *testing.T, op *Operator) {
		t.Helper()
		ctx := context.Background()
		off := dnsOnlyGroup("d1")
		off.Resolve.Fallback = boolPtr(false)
		if _, err := op.ApplyGroups(ctx, []Group{off}); err != nil {
			t.Fatal(err)
		}
		if _, err := op.ApplyGroups(ctx, []Group{dnsOnlyGroup("d1")}); err != nil {
			t.Fatal(err)
		}
		grps, err := op.LoadGroupsFile()
		if err != nil || len(grps) != 1 || grps[0].Resolve.Fallback == nil || *grps[0].Resolve.Fallback {
			t.Fatalf("fallback lost: %+v %v", grps, err)
		}
	}
	t.Run("offline", func(t *testing.T) { check(t, newGroupsOperator(t)) })
	t.Run("live", func(t *testing.T) {
		mockSrv, handler := newMockFircHandler()
		srv := httptest.NewServer(handler)
		defer srv.Close()
		op := newGroupsOperator(t)
		op.SetClient(NewHTTPClient(srv.URL, srv.Client()))
		op.SetNetfilterStrictHook(func(context.Context, ExtConfig, []Group) error { return nil })
		check(t, op)
		mockSrv.mu.Lock()
		defer mockSrv.mu.Unlock()
		if f := mockSrv.groups[0].Resolve.Fallback; f == nil || *f {
			t.Fatalf("runtime fallback=%v", f)
		}
	})
	t.Run("staged", func(t *testing.T) {
		op := newGroupsOperator(t)
		off := dnsOnlyGroup("d1")
		off.Resolve.Fallback = boolPtr(false)
		for _, g := range []Group{off, dnsOnlyGroup("d1")} {
			if _, err := op.StageGroupsOffline([]Group{g}); err != nil {
				t.Fatal(err)
			}
		}
		if out, _ := os.ReadFile(op.GroupsPath()); !strings.Contains(string(out), "fallback: false") {
			t.Fatalf("fallback lost:\n%s", out)
		}
	})
}

// «dns» в настройках не попадает туда, где нужен выход: Global, KeenDNS, прокси-группы.
func TestExtConfig_DropsDNSOnlyInterface(t *testing.T) {
	if _, ok := globalGroup(ExtConfig{GlobalTarget: "iface:dns"}); ok {
		t.Error("Global built on a dns group")
	}
	ext := normalizeExtConfig(ExtConfig{
		ProxyGroups:  []ProxyGroup{{ID: "pg", Interfaces: []string{"dns", "nwg0"}}},
		KeenDNSCloud: KeenDNSCloudConfig{Enabled: true, Interfaces: []string{"dns"}},
	})
	if got := ext.ProxyGroups[0].Interfaces; len(got) != 1 || got[0] != "nwg0" {
		t.Errorf("proxy group interfaces=%v", got)
	}
	if extWantsKeenDNS(ext) {
		t.Errorf("KeenDNS interfaces=%v", ext.KeenDNSCloud.Interfaces)
	}
}

func TestAddConfirmedCandidate_RefusesDNSOnlyGroup(t *testing.T) {
	op := newGroupsOperator(t)
	if err := op.SaveGroupsFile([]Group{dnsOnlyGroup("d1")}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := op.AddConfirmedCandidate(context.Background(), "blocked.example", "d1"); !errors.Is(err, ErrCandidateGroupDNSOnly) {
		t.Fatalf("err=%v", err)
	}
}

// Тег "dns" sing-box без туннеля не превращается в группу «только DNS», а такая группа
// при обратной миграции пропускается, а не валит её.
func TestMigration_SkipsDNSOnlyGroups(t *testing.T) {
	groups, _ := migrateSingboxRouterFragment([]byte(`{"route":{"rules":[{"domain":["a.example"],"outbound":"dns"},{"domain":["b.example"],"outbound":"vpn"}]}}`),
		map[string]string{"vpn": "tunvless0"}, nil)
	if len(groups) != 1 || groups[0].Interface[0] != "tunvless0" {
		t.Errorf("migrated=%+v", groups)
	}
	g := dnsOnlyGroup("d1")
	g.Rules = []Rule{{Type: "namespace", Rule: "a.example", Enable: true}}
	res, _, _, _, _, err := buildReverseSingboxPayloads(nil, []Group{g}, "", nil, true)
	if err != nil || len(res.Skipped) != 1 || len(res.Groups) != 0 {
		t.Errorf("reverse: res=%+v err=%v", res, err)
	}
}

func TestValidateFircGroupsYAML_RefusesInvalidDNSGroup(t *testing.T) {
	if _, err := validateFircGroupsYAML([]byte("groups:\n  - id: g\n    name: g\n    interface: dns\n"), nil); err == nil {
		t.Fatal("a dns group without resolve.server was accepted")
	}
}

// Резолвер внутри пула из firc.conf самого снимка: такой groups.yaml демон после восстановления не загрузит.
func TestSnapshotPreflight_RefusesResolverInPool(t *testing.T) {
	op := newGroupsOperator(t)
	const groups = "groups:\n  - id: 0a1b2c3d\n    name: g\n    interface: nwg0\n    resolve:\n      server: 100.64.0.1\n"
	entry := func(rel, data string) SnapshotFileEntry {
		return SnapshotFileEntry{Scope: "firc", RelPath: rel, DataB64: base64.StdEncoding.EncodeToString([]byte(data))}
	}
	preflight := func(entries ...SnapshotFileEntry) error {
		_, _, err := op.validateSnapshotEntriesPreflight(context.Background(), "", entries, false, "")
		return err
	}
	if err := preflight(entry("groups.yaml", groups)); err != nil {
		t.Fatalf("outside the default pool: %v", err)
	}
	conf := "configVersion: 0.7.0\napp:\n  addressPool:\n    v4:\n      pool: 100.64.0.0/15\n"
	var invalid *GroupValidationError
	if err := preflight(entry("groups.yaml", groups), entry("firc.conf", conf)); !errors.As(err, &invalid) || invalid.Field != "resolve.server" {
		t.Fatalf("pool of the snapshot's firc.conf: err=%v", err)
	}
	if err := os.WriteFile(op.ConfigPath(), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := preflight(entry("groups.yaml", groups)); !errors.As(err, &invalid) {
		t.Fatalf("pool of the firc.conf on disk: err=%v", err)
	}
}
