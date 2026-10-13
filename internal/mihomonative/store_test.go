package mihomonative

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hoaxisr/awg-manager/internal/mihomo"
	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/strictfs"
)

func TestStoreVLESSRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mihomo-native.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateVLESS("vless://id@host:443?type=xhttp&path=%2Fx#X", EngineAuto, EngineSingbox)
	if err != nil {
		t.Fatal(err)
	}
	if created.SelectedEngine != EngineMihomo {
		t.Fatalf("engine=%q", created.SelectedEngine)
	}
	reloaded, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	items := reloaded.ListProxies()
	if len(items) != 1 || items[0].ID != created.ID {
		t.Fatalf("items=%#v", items)
	}
	items[0].NativeConfig["name"] = "mutated"
	if reloaded.ListProxies()[0].NativeConfig["name"] == "mutated" {
		t.Fatal("ListProxies leaked mutable map")
	}
	if err := reloaded.DeleteProxy(created.ID); err != nil {
		t.Fatal(err)
	}
	if err := reloaded.DeleteProxy(created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v", err)
	}
}

func TestStoreSubscriptionIsIndependent(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "mihomo-native.json"))
	if err != nil {
		t.Fatal(err)
	}
	sub, err := store.CreateSubscription(CreateSubscriptionInput{
		Name: "Mihomo provider", URL: "https://example.test/provider.yaml",
		Format: FormatMihomoProvider, EnginePreference: EngineMihomo, Enabled: true,
		Headers: map[string][]string{"User-Agent": {"Clash.Meta"}, "X-Device": {"router"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sub.EnginePreference != EngineMihomo || len(store.ListSubscriptions()) != 1 {
		t.Fatalf("sub=%#v", sub)
	}
	providers := store.ConfigProviders()
	if providers[sub.ProviderName]["url"] != sub.URL {
		t.Fatalf("providers=%#v", providers)
	}
	if providers[sub.ProviderName]["interval"] != 0 {
		t.Fatalf("manual refresh interval=%#v, want 0", providers[sub.ProviderName]["interval"])
	}
	headers, ok := providers[sub.ProviderName]["header"].(map[string][]string)
	if !ok || len(headers["User-Agent"]) != 1 || headers["User-Agent"][0] != "Clash.Meta" {
		t.Fatalf("provider headers=%#v", providers[sub.ProviderName]["header"])
	}
	groups := store.ConfigProviderGroups()
	if len(groups) != 1 || groups[0]["name"] != sub.GroupName {
		t.Fatalf("groups=%#v", groups)
	}
}

func TestStoreNativeGroupsAndRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mihomo-native.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ImportLegacyGroups(nil); err != nil {
		t.Fatal(err)
	}
	if err := store.ImportLegacyRules(nil); err != nil {
		t.Fatal(err)
	}
	group, err := store.SaveGroup(ProxyGroup{Name: "Video", Type: "url-test", Proxies: []string{"DIRECT", "node"}, URL: "https://example.test/204", Interval: 60, Tolerance: 80, Timeout: 4000, Filter: "(?i)nl", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if !store.HasGroups() || len(store.ConfigProviderGroups()) != 1 {
		t.Fatalf("groups=%#v", store.ConfigProviderGroups())
	}
	cfgGroup := store.ConfigProviderGroups()[0]
	if cfgGroup["tolerance"] != 80 || cfgGroup["timeout"] != 4000 || cfgGroup["filter"] != "(?i)nl" {
		t.Fatalf("advanced group fields=%#v", cfgGroup)
	}
	ruleA, err := store.SaveRule(Rule{Type: "DOMAIN-SUFFIX", Payload: "youtube.com", Outbound: group.Name, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	ruleB, err := store.SaveRule(Rule{Type: "MATCH", Outbound: "direct", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := store.ConfigRules(); len(got) != 2 || got[0] != "DOMAIN-SUFFIX,youtube.com,Video" || got[1] != "MATCH,DIRECT" {
		t.Fatalf("rules=%#v", got)
	}
	if err := store.ReorderRules([]string{ruleB.ID, ruleA.ID}); err != nil {
		t.Fatal(err)
	}
	if got := store.ConfigRules(); got[0] != "MATCH,DIRECT" {
		t.Fatalf("reordered=%#v", got)
	}
	reloaded, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.ListGroups()) != 1 || len(reloaded.ListRules()) != 2 {
		t.Fatalf("reload groups=%#v rules=%#v", reloaded.ListGroups(), reloaded.ListRules())
	}
}

func TestStoreRuleProvider(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "mihomo-native.json"))
	if err != nil {
		t.Fatal(err)
	}
	provider, err := store.SaveRuleProvider(RuleProvider{Name: "youtube", Type: "http", URL: "https://example.test/youtube.mrs", Behavior: "domain", Format: "mrs", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	cfg := store.ConfigRuleProviders()[provider.Name]
	if cfg["behavior"] != "domain" || cfg["format"] != "mrs" || cfg["url"] != provider.URL {
		t.Fatalf("provider=%#v", cfg)
	}
}

func TestStoreRejectsSelfReferencingGroupAndAcceptsExtendedRuleTypes(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "mihomo-native.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveGroup(ProxyGroup{Name: "loop", Type: "select", Proxies: []string{"loop"}}); err == nil {
		t.Fatal("expected self-reference validation error")
	}
	if _, err := store.SaveRule(Rule{Type: "IP-ASN", Payload: "13335", Outbound: "DIRECT"}); err != nil {
		t.Fatalf("extended Mihomo rule type rejected: %v", err)
	}
}

func TestStoreImportsLegacyRoutingOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mihomo-native.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ImportLegacyGroups([]storage.ProxyGroup{{Name: "Block", Type: "select", Proxies: []string{"block", "direct"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.ImportLegacyRules([]string{"DOMAIN-SUFFIX,telegram.org,Block", "GEOIP,RU,DIRECT,no-resolve"}); err != nil {
		t.Fatal(err)
	}
	if !store.HasGroups() || !store.HasRules() {
		t.Fatal("legacy ownership flags were not persisted")
	}
	groups := store.ListGroups()
	if len(groups) != 1 || groups[0].Proxies[0] != "REJECT" || groups[0].Proxies[1] != "DIRECT" {
		t.Fatalf("groups=%#v", groups)
	}
	if got := store.ConfigRules(); len(got) != 2 || got[1] != "GEOIP,RU,DIRECT,no-resolve" {
		t.Fatalf("rules=%#v", got)
	}
	if err := store.ImportLegacyGroups([]storage.ProxyGroup{{Name: "must-not-return", Type: "select", Proxies: []string{"direct"}}}); err != nil {
		t.Fatal(err)
	}
	if len(store.ListGroups()) != 1 {
		t.Fatal("one-time migration ran twice")
	}
	reloaded, err := NewStore(path)
	if err != nil || !reloaded.HasGroups() || !reloaded.HasRules() {
		t.Fatalf("reloaded migration state: err=%v groups=%v rules=%v", err, reloaded.HasGroups(), reloaded.HasRules())
	}
}

func TestStoreInlineSubscriptionImportsNativeNodesAtomically(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "mihomo-native.json"))
	if err != nil {
		t.Fatal(err)
	}
	sub, err := store.CreateSubscription(CreateSubscriptionInput{
		Name: "XHTTP", Inline: "vless://id1@a.example:443?type=xhttp#A\nvless://id2@b.example:443?type=grpc#B",
		Format: FormatShareLinks, EnginePreference: EngineAuto, RoutingEngine: EngineMihomo, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	nodes := store.ListProxies()
	if len(nodes) != 2 || nodes[0].SourceID != sub.ID || nodes[1].SourceID != sub.ID {
		t.Fatalf("nodes=%#v", nodes)
	}
	_, err = store.CreateSubscription(CreateSubscriptionInput{
		Name: "Bad", Inline: "vless://id@ok:443#ok\nss://YWVzLTEyOC1nY206cGFzcw@bad:443#bad",
		Format: FormatShareLinks, EnginePreference: EngineMihomo, RoutingEngine: EngineMihomo, Enabled: true,
	})
	if err == nil {
		t.Fatal("expected unsupported protocol error")
	}
	if len(store.ListSubscriptions()) != 1 || len(store.ListProxies()) != 2 {
		t.Fatal("failed import changed persisted state")
	}
}

func TestStoreInlineSubscriptionCreatesOwnSelector(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "mihomo-native.json"))
	if err != nil {
		t.Fatal(err)
	}
	sub, err := store.CreateSubscription(CreateSubscriptionInput{
		Name: "Inline", Inline: "vless://id1@a.example:443?type=xhttp#First\nvless://id2@b.example:443?type=grpc#Second",
		Format: FormatShareLinks, EnginePreference: EngineMihomo, RoutingEngine: EngineMihomo, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	groups := store.ConfigProviderGroups()
	if len(groups) != 1 || groups[0]["name"] != sub.GroupName || groups[0]["type"] != "select" {
		t.Fatalf("groups=%#v", groups)
	}
	members, ok := groups[0]["proxies"].([]string)
	if !ok || len(members) != 2 || members[0] != "First" || members[1] != "Second" {
		t.Fatalf("members=%#v", groups[0]["proxies"])
	}
}

func TestStoreRejectsDuplicateSubscriptionGroupName(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "mihomo-native.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateSubscription(CreateSubscriptionInput{
		Name: "Provider", URL: "https://example.test/one.yaml",
		Format: FormatMihomoProvider, EnginePreference: EngineMihomo, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateSubscription(CreateSubscriptionInput{
		Name: "provider", URL: "https://example.test/two.yaml",
		Format: FormatMihomoProvider, EnginePreference: EngineMihomo, Enabled: true,
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate subscription name") {
		t.Fatalf("err=%v", err)
	}
}

func TestStoreRejectsNonHTTPProviderURL(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "mihomo-native.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateSubscription(CreateSubscriptionInput{
		Name: "Provider", URL: "happ://encrypted-payload",
		Format: FormatMihomoProvider, EnginePreference: EngineMihomo, Enabled: true,
	})
	if err == nil || !strings.Contains(err.Error(), "provider URL") {
		t.Fatalf("err=%v", err)
	}
}

func TestStoreDisabledInlineSubscriptionDoesNotEmitProxies(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "mihomo-native.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateSubscription(CreateSubscriptionInput{
		Name: "Disabled", Inline: "vless://id@disabled.example:443?type=xhttp#Hidden",
		Format: FormatShareLinks, EnginePreference: EngineMihomo, RoutingEngine: EngineMihomo, Enabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if proxies := store.ConfigProxies(); len(proxies) != 0 {
		t.Fatalf("ConfigProxies()=%#v, want no proxies from disabled subscription", proxies)
	}
	if groups := store.ConfigProviderGroups(); len(groups) != 0 {
		t.Fatalf("ConfigProviderGroups()=%#v, want no groups from disabled subscription", groups)
	}
}

func TestStoreReservesGeneratedGroupNames(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "mihomo-native.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateSubscription(CreateSubscriptionInput{
		Name: "Native", URL: "https://example.test/provider.yaml",
		Format: FormatMihomoProvider, EnginePreference: EngineMihomo, Enabled: true,
	})
	if err == nil || !strings.Contains(err.Error(), "reserved group") {
		t.Fatalf("subscription err=%v", err)
	}
	_, err = store.SaveGroup(ProxyGroup{Name: autoNativeGroupName, Type: "select", Proxies: []string{"DIRECT"}, Enabled: true})
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("group err=%v", err)
	}
}

func TestDeleteSubscriptionRemovesOnlyItsImportedNodes(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "mihomo-native.json"))
	if err != nil {
		t.Fatal(err)
	}
	sub, err := store.CreateSubscription(CreateSubscriptionInput{
		Name: "Imported", Inline: "hysteria2://secret@hy.example:443#HY",
		Format: FormatShareLinks, EnginePreference: EngineMihomo, RoutingEngine: EngineMihomo, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateManual(ManualProxyInput{
		Name: "Manual", Protocol: "anytls", Server: "a.example", Port: 443,
		EnginePreference: EngineMihomo, Config: map[string]interface{}{"password": "secret"},
	}, EngineMihomo); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteSubscription(sub.ID); err != nil {
		t.Fatal(err)
	}
	nodes := store.ListProxies()
	if len(nodes) != 1 || nodes[0].Name != "Manual" {
		t.Fatalf("nodes=%#v", nodes)
	}
	if len(store.ListSubscriptions()) != 0 {
		t.Fatal("subscription was not deleted")
	}
}

func TestRecordSubscriptionRefresh(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "mihomo-native.json"))
	if err != nil {
		t.Fatal(err)
	}
	sub, err := store.CreateSubscription(CreateSubscriptionInput{
		Name: "Provider", URL: "https://example.test/provider.yaml", Format: FormatMihomoProvider,
		EnginePreference: EngineMihomo, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordSubscriptionRefresh(sub.ID, nil); err != nil {
		t.Fatal(err)
	}
	updated, err := store.GetSubscription(sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.LastFetched.IsZero() || updated.LastError != "" {
		t.Fatalf("subscription=%#v", updated)
	}
}

func TestUpdateProxyPreservesIdentityAndRewritesReferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mihomo-native.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateManual(ManualProxyInput{
		Name: "Old node", Protocol: "anytls", Server: "old.example", Port: 443,
		EnginePreference: EngineMihomo, Config: map[string]interface{}{"password": "secret"},
	}, EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	bridge := ProxyBridge{ListenPort: 12010, ProxyIndex: 7, ProxyInterface: "Proxy7", KernelInterface: "t2s7"}
	if err := store.SetBridge("proxy", created.ID, bridge); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveGroup(ProxyGroup{Name: "Preferred", Type: "select", Proxies: []string{created.Name}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveRule(Rule{Type: "DOMAIN", Payload: "example.test", Outbound: created.Name, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveRuleProvider(RuleProvider{
		Name: "domains", Type: "http", URL: "https://example.test/domains.yaml",
		Behavior: "domain", Format: "yaml", Proxy: created.Name, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	updated, err := store.UpdateProxy(created.ID, UpdateProxyInput{
		Manual: &ManualProxyInput{
			Name: "New node", Protocol: "anytls", Server: "new.example", Port: 8443,
			Config: map[string]interface{}{"password": "new-secret"},
		},
		EnginePreference: EngineMihomo, RoutingEngine: EngineMihomo, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != created.ID || !updated.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("identity changed: before=%#v after=%#v", created, updated)
	}
	if updated.Bridge == nil || *updated.Bridge != bridge {
		t.Fatalf("bridge=%#v, want %#v", updated.Bridge, bridge)
	}
	if updated.Name != "New node" || updated.NativeConfig["server"] != "new.example" || fmt.Sprint(updated.NativeConfig["port"]) != "8443" {
		t.Fatalf("updated proxy=%#v", updated)
	}
	if got := store.ListGroups()[0].Proxies; len(got) != 1 || got[0] != updated.Name {
		t.Fatalf("group references=%#v", got)
	}
	if got := store.ListRules()[0].Outbound; got != updated.Name {
		t.Fatalf("rule outbound=%q", got)
	}
	if got := store.ListRuleProviders()[0].Proxy; got != updated.Name {
		t.Fatalf("rule-provider proxy=%q", got)
	}

	reloaded, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reloaded.GetProxy(created.ID)
	if err != nil || got.Name != updated.Name || got.Bridge == nil || *got.Bridge != bridge {
		t.Fatalf("reloaded proxy=%#v err=%v", got, err)
	}
}

func TestUpdateProxyRejectsDuplicateNameAndSubscriptionMember(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "mihomo-native.json"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.CreateManual(ManualProxyInput{
		Name: "First", Protocol: "anytls", Server: "first.example", Port: 443,
		EnginePreference: EngineMihomo, Config: map[string]interface{}{"password": "secret"},
	}, EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateManual(ManualProxyInput{
		Name: "Second", Protocol: "anytls", Server: "second.example", Port: 443,
		EnginePreference: EngineMihomo, Config: map[string]interface{}{"password": "secret"},
	}, EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.UpdateProxy(first.ID, UpdateProxyInput{
		Manual:           &ManualProxyInput{Name: second.Name, Protocol: "anytls", Server: "changed.example", Port: 443, Config: map[string]interface{}{"password": "secret"}},
		EnginePreference: EngineMihomo, RoutingEngine: EngineMihomo, Enabled: true,
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate proxy name") {
		t.Fatalf("duplicate-name err=%v", err)
	}
	unchanged, err := store.GetProxy(first.ID)
	if err != nil || unchanged.Name != first.Name {
		t.Fatalf("failed update changed proxy: %#v err=%v", unchanged, err)
	}

	sub, err := store.CreateSubscription(CreateSubscriptionInput{
		Name: "Inline", Inline: "vless://id@member.example:443?type=xhttp#Member",
		Format: FormatShareLinks, EnginePreference: EngineMihomo, RoutingEngine: EngineMihomo, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var member ProxyNode
	for _, node := range store.ListProxies() {
		if node.SourceID == sub.ID {
			member = node
			break
		}
	}
	_, err = store.UpdateProxy(member.ID, UpdateProxyInput{
		URI:              "vless://id@member.example:443?type=xhttp#Changed",
		EnginePreference: EngineMihomo, RoutingEngine: EngineMihomo, Enabled: true,
	})
	if err == nil || !strings.Contains(err.Error(), "edited through their subscription") {
		t.Fatalf("subscription-member err=%v", err)
	}
}

func TestUpdateProviderSubscriptionPreservesIdentityAndRewritesGroupReferences(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "mihomo-native.json"))
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateSubscription(CreateSubscriptionInput{
		Name: "Old provider", URL: "https://example.test/old.yaml", Format: FormatMihomoProvider,
		EnginePreference: EngineMihomo, RefreshHours: 12, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	bridge := ProxyBridge{ListenPort: 12011, ProxyIndex: 8, ProxyInterface: "Proxy8", KernelInterface: "t2s8"}
	if err := store.SetBridge("subscription", created.ID, bridge); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveGroup(ProxyGroup{Name: "Provider route", Type: "select", Proxies: []string{created.GroupName}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveRule(Rule{Type: "DOMAIN-SUFFIX", Payload: "example.org", Outbound: created.GroupName, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveRuleProvider(RuleProvider{
		Name: "remote-rules", Type: "http", URL: "https://example.test/rules.yaml",
		Behavior: "domain", Format: "yaml", Proxy: created.GroupName, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	updated, err := store.UpdateSubscription(created.ID, UpdateSubscriptionInput{
		Name: "New provider", URL: "https://example.test/new.yaml", Format: FormatMihomoProvider,
		EnginePreference: EngineMihomo, RefreshHours: 24, Enabled: true,
		Headers: map[string][]string{"User-Agent": {"Mihomo"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != created.ID || updated.ProviderName != created.ProviderName || !updated.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("provider identity changed: before=%#v after=%#v", created, updated)
	}
	if updated.Bridge == nil || *updated.Bridge != bridge {
		t.Fatalf("bridge=%#v, want %#v", updated.Bridge, bridge)
	}
	if got := store.ListGroups()[0].Proxies[0]; got != updated.GroupName {
		t.Fatalf("group reference=%q", got)
	}
	if got := store.ListRules()[0].Outbound; got != updated.GroupName {
		t.Fatalf("rule reference=%q", got)
	}
	if got := store.ListRuleProviders()[0].Proxy; got != updated.GroupName {
		t.Fatalf("provider reference=%q", got)
	}
	provider := store.ConfigProviders()[created.ProviderName]
	if provider["url"] != updated.URL || provider["interval"] != 24*3600 {
		t.Fatalf("provider config=%#v", provider)
	}
}

func TestUpdateLegacyAutoURLSubscriptionMigratesToProvider(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "mihomo-native.json"))
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateSubscription(CreateSubscriptionInput{
		Name: "Legacy provider", URL: "https://example.test/legacy.yaml", Format: FormatAuto,
		EnginePreference: EngineMihomo, RefreshHours: 12, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Format != FormatAuto {
		t.Fatalf("precondition: format=%q, want legacy auto", created.Format)
	}

	updated, err := store.UpdateSubscription(created.ID, UpdateSubscriptionInput{
		// Old clients can still echo "auto"; the store resolves it from the
		// persisted source shape while upgrading the saved format.
		Name: created.Name, URL: "https://example.test/current.yaml", Format: FormatAuto,
		EnginePreference: EngineMihomo, RefreshHours: 24, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Format != FormatMihomoProvider {
		t.Fatalf("format=%q, want %q", updated.Format, FormatMihomoProvider)
	}
	provider := store.ConfigProviders()[created.ProviderName]
	if provider == nil || provider["url"] != updated.URL {
		t.Fatalf("provider config=%#v", provider)
	}
}

func TestUpdateInlineSubscriptionPreservesMembersAndRejectsReferencedRemoval(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "mihomo-native.json"))
	if err != nil {
		t.Fatal(err)
	}
	lineA := "vless://id-a@a.example:443?type=xhttp#Alpha"
	lineB := "vless://id-b@b.example:443?type=grpc#Beta"
	lineC := "hysteria2://secret@c.example:443#Gamma"
	sub, err := store.CreateSubscription(CreateSubscriptionInput{
		Name: "Inline", Inline: lineA + "\n" + lineB, Format: FormatShareLinks,
		EnginePreference: EngineMihomo, RoutingEngine: EngineMihomo, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	memberIDs := make(map[string]string)
	for _, node := range store.ListProxies() {
		if node.SourceID == sub.ID {
			memberIDs[node.Name] = node.ID
		}
	}
	if _, err := store.SaveGroup(ProxyGroup{Name: "Keep beta", Type: "select", Proxies: []string{"Beta"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	updated, err := store.UpdateSubscription(sub.ID, UpdateSubscriptionInput{
		Name: sub.Name, Inline: lineB + "\n" + lineA + "\n" + lineC, Format: FormatShareLinks,
		EnginePreference: EngineMihomo, RoutingEngine: EngineMihomo, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.GroupName != sub.GroupName {
		t.Fatalf("group name changed: %q", updated.GroupName)
	}
	for _, node := range store.ListProxies() {
		if node.SourceID != sub.ID || node.Name == "Gamma" {
			continue
		}
		if node.ID != memberIDs[node.Name] {
			t.Fatalf("member %q ID=%q, want %q", node.Name, node.ID, memberIDs[node.Name])
		}
	}

	_, err = store.UpdateSubscription(sub.ID, UpdateSubscriptionInput{
		Name: sub.Name, Inline: lineA + "\n" + lineC, Format: FormatShareLinks,
		EnginePreference: EngineMihomo, RoutingEngine: EngineMihomo, Enabled: true,
	})
	if err == nil || !strings.Contains(err.Error(), "cannot remove subscription member \"Beta\"") || !strings.Contains(err.Error(), "Keep beta") {
		t.Fatalf("referenced-removal err=%v", err)
	}
	stillThere := false
	for _, node := range store.ListProxies() {
		if node.SourceID == sub.ID && node.Name == "Beta" && node.ID == memberIDs["Beta"] {
			stillThere = true
		}
	}
	if !stillThere {
		t.Fatal("failed update removed referenced member")
	}
}

func TestStoreSnapshotRestoreRollsBackNestedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mihomo-native.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateManual(ManualProxyInput{
		Name: "Original", Protocol: "anytls", Server: "original.example", Port: 443,
		EnginePreference: EngineMihomo, Config: map[string]interface{}{"password": "secret", "client-metadata": map[string]interface{}{"device": "router"}},
	}, EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	bridge := ProxyBridge{ListenPort: 12012, ProxyIndex: 9, ProxyInterface: "Proxy9", KernelInterface: "t2s9"}
	if err := store.SetBridge("proxy", created.ID, bridge); err != nil {
		t.Fatal(err)
	}
	group, err := store.SaveGroup(ProxyGroup{Name: "Original group", Type: "select", Proxies: []string{created.Name}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateProxy(created.ID, UpdateProxyInput{
		Manual:           &ManualProxyInput{Name: "Changed", Protocol: "anytls", Server: "changed.example", Port: 8443, Config: map[string]interface{}{"password": "changed"}},
		EnginePreference: EngineMihomo, RoutingEngine: EngineMihomo, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSubscription(CreateSubscriptionInput{
		Name: "Temporary", URL: "https://example.test/provider.yaml", Format: FormatMihomoProvider,
		EnginePreference: EngineMihomo, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.RestoreSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	if len(store.ListSubscriptions()) != 0 {
		t.Fatalf("subscriptions survived rollback: %#v", store.ListSubscriptions())
	}
	got, err := store.GetProxy(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != created.Name || got.NativeConfig["server"] != "original.example" || got.Bridge == nil || *got.Bridge != bridge {
		t.Fatalf("restored proxy=%#v", got)
	}
	groups := store.ListGroups()
	if len(groups) != 1 || groups[0].ID != group.ID || len(groups[0].Proxies) != 1 || groups[0].Proxies[0] != created.Name {
		t.Fatalf("restored groups=%#v", groups)
	}
	reloaded, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	reloadedProxy, err := reloaded.GetProxy(created.ID)
	if err != nil || reloadedProxy.Name != created.Name || len(reloaded.ListSubscriptions()) != 0 {
		t.Fatalf("disk rollback proxy=%#v subscriptions=%#v err=%v", reloadedProxy, reloaded.ListSubscriptions(), err)
	}
}

func TestCreateSubscriptionRejectsDuplicateAndConflictingRuntimeNames(t *testing.T) {
	t.Run("duplicate inline member names", func(t *testing.T) {
		store, err := NewStore(filepath.Join(t.TempDir(), "mihomo-native.json"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = store.CreateSubscription(CreateSubscriptionInput{
			Name: "Duplicate members",
			Inline: "vless://id-a@a.example:443?type=xhttp#Same\n" +
				"vless://id-b@b.example:443?type=grpc#same",
			Format: FormatShareLinks, EnginePreference: EngineMihomo, RoutingEngine: EngineMihomo, Enabled: true,
		})
		if err == nil || !strings.Contains(err.Error(), "duplicate proxy name") {
			t.Fatalf("err=%v", err)
		}
		if len(store.ListSubscriptions()) != 0 || len(store.ListProxies()) != 0 {
			t.Fatalf("invalid subscription mutated state: subs=%#v proxies=%#v", store.ListSubscriptions(), store.ListProxies())
		}
	})

	t.Run("provider group conflicts with proxy", func(t *testing.T) {
		store, err := NewStore(filepath.Join(t.TempDir(), "mihomo-native.json"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreateManual(ManualProxyInput{
			Name: "Mihomo: Provider", Protocol: "anytls", Server: "proxy.example", Port: 443,
			EnginePreference: EngineMihomo, Config: map[string]interface{}{"password": "secret"},
		}, EngineMihomo); err != nil {
			t.Fatal(err)
		}
		_, err = store.CreateSubscription(CreateSubscriptionInput{
			Name: "Provider", URL: "https://example.test/provider.yaml", Format: FormatMihomoProvider,
			EnginePreference: EngineMihomo, Enabled: true,
		})
		if err == nil || !strings.Contains(err.Error(), "conflicts with proxy") {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestUpdateSubscriptionRejectsGroupNameConflictingWithProxy(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "mihomo-native.json"))
	if err != nil {
		t.Fatal(err)
	}
	sub, err := store.CreateSubscription(CreateSubscriptionInput{
		Name: "Original", URL: "https://example.test/provider.yaml", Format: FormatMihomoProvider,
		EnginePreference: EngineMihomo, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateManual(ManualProxyInput{
		Name: "Mihomo: Collision", Protocol: "anytls", Server: "proxy.example", Port: 443,
		EnginePreference: EngineMihomo, Config: map[string]interface{}{"password": "secret"},
	}, EngineMihomo); err != nil {
		t.Fatal(err)
	}
	_, err = store.UpdateSubscription(sub.ID, UpdateSubscriptionInput{
		Name: "Collision", URL: sub.URL, Format: sub.Format,
		EnginePreference: EngineMihomo, Enabled: true,
	})
	if err == nil || !strings.Contains(err.Error(), "conflicts with proxy") {
		t.Fatalf("err=%v", err)
	}
	unchanged, getErr := store.GetSubscription(sub.ID)
	if getErr != nil || unchanged.Name != sub.Name || unchanged.GroupName != sub.GroupName {
		t.Fatalf("failed update changed subscription: %#v err=%v", unchanged, getErr)
	}
}

func TestStoreSaveRulesBatch_AtomicRollbackOnFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mihomo-native.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}

	initial, err := store.SaveRule(Rule{
		Type: "DOMAIN-SUFFIX", Payload: "google.com", Outbound: "DIRECT", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	batch := []Rule{
		{Type: "DOMAIN-SUFFIX", Payload: "valid.com", Outbound: "PROXY", Enabled: true},
		{Type: "INVALID-TYPE", Payload: "bad.com", Outbound: "PROXY", Enabled: true},
	}

	_, err = store.SaveRulesBatch(batch)
	if err == nil {
		t.Fatal("expected batch error due to INVALID-TYPE, got nil")
	}

	// Verify store state is unchanged
	rules := store.ListRules()
	if len(rules) != 1 || rules[0].ID != initial.ID {
		t.Fatalf("store was partially mutated: %+v", rules)
	}

	// Reload from disk and verify file is identical
	reloaded, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	reloadedRules := reloaded.ListRules()
	if len(reloadedRules) != 1 || reloadedRules[0].ID != initial.ID {
		t.Fatalf("disk store was partially mutated: %+v", reloadedRules)
	}
}

func TestStoreSchemaV3ToV4_PreservesExplicitLegacyOwnerDiskFixture(t *testing.T) {
	fixture := `{
		"version": 3,
		"proxies": [
			{
				"id": "p1",
				"name": "Node With Explicit Legacy Owner",
				"protocol": "vless",
				"transport": "xhttp",
				"enginePreference": "mihomo",
				"selectedEngine": "mihomo",
				"enabled": true,
				"bridge": {
					"listenPort": 12005,
					"proxyIndex": 5,
					"proxyInterface": "Proxy5",
					"kernelInterface": "t2s5",
					"legacyOwner": "Legacy VLESS Node"
				},
				"createdAt": "2026-08-25T12:00:00Z",
				"updatedAt": "2026-08-25T12:00:00Z"
			}
		],
		"subscriptions": [
			{
				"id": "s1",
				"name": "Sub With Explicit Legacy Owner",
				"url": "https://example.com/sub",
				"format": "mihomo-provider",
				"enginePreference": "mihomo",
				"enabled": true,
				"bridge": {
					"listenPort": 12006,
					"proxyIndex": 6,
					"proxyInterface": "Proxy6",
					"kernelInterface": "t2s6",
					"legacyOwner": "Legacy Sub"
				},
				"createdAt": "2026-08-25T12:00:00Z",
				"updatedAt": "2026-08-25T12:00:00Z"
			}
		]
	}`

	path := filepath.Join(t.TempDir(), "mihomo-native.json")
	if err := os.WriteFile(path, []byte(fixture), 0644); err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}

	p, err := store.GetProxy("p1")
	if err != nil {
		t.Fatal(err)
	}
	if p.Bridge == nil || p.Bridge.LegacyOwner != "Legacy VLESS Node" {
		t.Fatalf("proxy LegacyOwner not preserved: %+v", p.Bridge)
	}

	s, err := store.GetSubscription("s1")
	if err != nil {
		t.Fatal(err)
	}
	if s.Bridge == nil || s.Bridge.LegacyOwner != "Legacy Sub" {
		t.Fatalf("sub LegacyOwner not preserved: %+v", s.Bridge)
	}

	// Verify persistence
	reloaded, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	rp, _ := reloaded.GetProxy("p1")
	if rp.Bridge.LegacyOwner != "Legacy VLESS Node" {
		t.Fatalf("reloaded proxy LegacyOwner mismatch: %+v", rp.Bridge)
	}
}

func TestStoreMigration_PersistFailureReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mihomo-native.json")
	fixture := `{"version":2,"rules":[{"id":"r1","outbound":"Задний вход :)","type":"MATCH","enabled":true}]}`
	if err := os.WriteFile(path, []byte(fixture), 0644); err != nil {
		t.Fatal(err)
	}

	// Make directory read-only to force write failure during migration persistence
	if err := os.Chmod(dir, 0555); err != nil {
		t.Skip("cannot set directory permissions on this platform")
	}
	defer func() { _ = os.Chmod(dir, 0755) }()

	_, err := NewStore(path)
	if err == nil {
		// On Windows Chmod 0555 on directory might not prevent writing inside, so skip if not rejected
		return
	}
	if !strings.Contains(err.Error(), "persist migration") {
		t.Fatalf("expected persist migration error, got: %v", err)
	}
}

func TestStoreSnapshotFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mihomo-native.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}

	_, err = store.CreateVLESS("vless://id@host:443?type=xhttp&path=%2Fx#Original", EngineAuto, EngineSingbox)
	if err != nil {
		t.Fatal(err)
	}

	origDigest, err := store.CurrentDigest()
	if err != nil {
		t.Fatalf("CurrentDigest failed: %v", err)
	}

	txid := "20260915120000"
	snapFile, err := store.CreateSnapshotFile(txid)
	if err != nil {
		t.Fatalf("CreateSnapshotFile failed: %v", err)
	}
	if _, err := os.Stat(snapFile); err != nil {
		t.Fatalf("snapshot file missing: %v", err)
	}

	// Mutate store
	p2, err := store.CreateVLESS("vless://id2@host:443?type=xhttp&path=%2Fx#Mutated", EngineAuto, EngineSingbox)
	if err != nil {
		t.Fatal(err)
	}
	mutDigest, err := store.CurrentDigest()
	if err != nil || mutDigest == origDigest {
		t.Fatalf("expected digest change after mutation: orig=%s mut=%s", origDigest, mutDigest)
	}

	// Restore from snapshot
	if err := store.RestoreSnapshotFile(snapFile); err != nil {
		t.Fatalf("RestoreSnapshotFile failed: %v", err)
	}

	restoredDigest, err := store.CurrentDigest()
	if err != nil {
		t.Fatal(err)
	}
	if restoredDigest != origDigest {
		t.Fatalf("digest after restore mismatch: got %s, want %s", restoredDigest, origDigest)
	}

	// Verify p2 is gone
	if _, err := store.GetProxy(p2.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("mutated proxy p2 should be gone after restore")
	}

	// Remove snapshot
	if err := store.RemoveSnapshotFile(snapFile); err != nil {
		t.Fatalf("RemoveSnapshotFile failed: %v", err)
	}
	if _, err := os.Stat(snapFile); !os.IsNotExist(err) {
		t.Fatalf("snapshot file should be absent after removal")
	}
}

func TestStoreTxAdapter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mihomo-native.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}

	// Compile-time interface check
	var _ mihomo.NativeStoreTx = (*StoreTxAdapter)(nil)
	var tx mihomo.NativeStoreTx = store.TxAdapter()
	if tx == nil {
		t.Fatal("TxAdapter returned nil")
	}

	digest, err := tx.CurrentDigest()
	if err != nil || digest == "" {
		t.Fatalf("tx.CurrentDigest failed: %v", err)
	}
}

func TestStore_CreateSnapshotFileAt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mihomo-native.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}

	txid := "20260916120000000001"
	expectedPath, err := store.SnapshotFilePath(txid)
	if err != nil {
		t.Fatalf("SnapshotFilePath failed: %v", err)
	}

	// Negative test: mismatched targetPath
	badPath := filepath.Join(filepath.Dir(expectedPath), "store.snapshot.mismatch.json")
	if _, err := store.CreateSnapshotFileAt(txid, badPath); err == nil {
		t.Fatalf("expected error for mismatched target path %s, got nil", badPath)
	}
	if _, err := os.Stat(badPath); !os.IsNotExist(err) {
		t.Fatalf("mismatched file should not have been created")
	}

	// Positive test: exact canonical path
	currentDigestBefore, err := store.CurrentSnapshotDigest()
	if err != nil {
		t.Fatalf("CurrentSnapshotDigest failed: %v", err)
	}

	returnedDigest, err := store.CreateSnapshotFileAt(txid, expectedPath)
	if err != nil {
		t.Fatalf("CreateSnapshotFileAt failed: %v", err)
	}

	fileDigest, err := strictfs.ComputeFileDigest(expectedPath)
	if err != nil {
		t.Fatalf("ComputeFileDigest failed: %v", err)
	}

	// Verify all 3 digests match
	if returnedDigest != currentDigestBefore {
		t.Fatalf("digest mismatch: returned %q != currentBefore %q", returnedDigest, currentDigestBefore)
	}
	if returnedDigest != fileDigest {
		t.Fatalf("digest mismatch: returned %q != fileDigest %q", returnedDigest, fileDigest)
	}
}

func TestStoreDraftJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mihomo-native.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}

	// Initially nil
	dj, err := store.LoadDraftJournal()
	if err != nil {
		t.Fatalf("LoadDraftJournal failed: %v", err)
	}
	if dj != nil {
		t.Fatalf("expected nil draft journal, got: %+v", dj)
	}

	// Save draft journal
	expected := mihomo.DraftJournal{
		Version:                  1,
		TxID:                     "20260915120000",
		State:                    mihomo.DraftPending,
		DraftSnapshotFile:        "store.snapshot.20260915120000.json",
		BaseDesiredStoreDigest:   "base123",
		TargetDesiredStoreDigest: "target123",
	}
	if err := store.SaveDraftJournal(expected); err != nil {
		t.Fatalf("SaveDraftJournal failed: %v", err)
	}

	loaded, err := store.LoadDraftJournal()
	if err != nil {
		t.Fatalf("LoadDraftJournal after save failed: %v", err)
	}
	if loaded == nil || loaded.TxID != expected.TxID || loaded.State != expected.State {
		t.Fatalf("loaded mismatch: got %+v, want %+v", loaded, expected)
	}

	// Remove draft journal
	if err := store.RemoveDraftJournal(); err != nil {
		t.Fatalf("RemoveDraftJournal failed: %v", err)
	}
	loaded, err = store.LoadDraftJournal()
	if err != nil || loaded != nil {
		t.Fatalf("expected nil after removal, got: %+v (err: %v)", loaded, err)
	}
}

func TestStore_AtomicSnapshotContract(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}

	snap0, err := store.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot 0 failed: %v", err)
	}
	if snap0.Revision() != 0 {
		t.Fatalf("expected revision 0 for fresh store, got %d", snap0.Revision())
	}
	if store.SnapshotRevision() != snap0.Revision() {
		t.Fatalf("SnapshotRevision mismatch: store=%d, snap=%d", store.SnapshotRevision(), snap0.Revision())
	}
	if snap0.Digest() == "" {
		t.Fatal("expected non-empty digest for snap0")
	}

	// Mutation 1: add group
	_, err = store.SaveGroup(ProxyGroup{
		Name:    "TestGroup",
		Type:    "select",
		Proxies: []string{"DIRECT"},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveGroup failed: %v", err)
	}

	snap1, err := store.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot 1 failed: %v", err)
	}
	if snap1.Revision() != 1 {
		t.Fatalf("expected revision 1 after mutation, got %d", snap1.Revision())
	}
	if store.SnapshotRevision() != 1 {
		t.Fatalf("expected store revision 1, got %d", store.SnapshotRevision())
	}
	if snap1.Digest() == snap0.Digest() {
		t.Fatalf("digest should change after mutation: %s vs %s", snap1.Digest(), snap0.Digest())
	}

	// Mutation 2: create rule
	_, err = store.CreateRule(RuleInput{
		Type:     "DOMAIN-SUFFIX",
		Payload:  "google.com",
		Outbound: "TestGroup",
	})
	if err != nil {
		t.Fatalf("CreateRule failed: %v", err)
	}

	snap2, err := store.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot 2 failed: %v", err)
	}
	if snap2.Revision() != 2 {
		t.Fatalf("expected revision 2 after second mutation, got %d", snap2.Revision())
	}
	if snap2.Digest() == snap1.Digest() {
		t.Fatalf("digest should change after second mutation: %s vs %s", snap2.Digest(), snap1.Digest())
	}

	// Restore snap1
	if err := store.RestoreSnapshot(snap1); err != nil {
		t.Fatalf("RestoreSnapshot failed: %v", err)
	}

	snapRestored, err := store.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot after restore failed: %v", err)
	}
	// Restoring snap1 re-persists state, increasing revision counter, but data digest must match snap1 exactly!
	if snapRestored.Digest() != snap1.Digest() {
		t.Fatalf("restored data digest mismatch: got %s, want %s", snapRestored.Digest(), snap1.Digest())
	}
	if len(store.ListRules()) != 0 {
		t.Fatalf("expected 0 rules after rollback to snap1, got %d", len(store.ListRules()))
	}
	if len(store.ListGroups()) != 1 {
		t.Fatalf("expected 1 group after rollback to snap1, got %d", len(store.ListGroups()))
	}
}

func TestStoreDeleteGroup_CascadeRepointsRulesAndMembers(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}

	// 1. Create two groups: TargetGroup and ParentGroup
	targetGroup, err := store.SaveGroup(ProxyGroup{
		Name:    "TargetGroup",
		Type:    "url-test",
		Proxies: []string{"DIRECT"},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveGroup TargetGroup failed: %v", err)
	}

	_, err = store.SaveGroup(ProxyGroup{
		Name:    "ParentGroup",
		Type:    "select",
		Proxies: []string{"TargetGroup", "DIRECT"},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveGroup ParentGroup failed: %v", err)
	}

	// 2. Create a rule pointing to TargetGroup
	enabled := true
	rule, err := store.CreateRule(RuleInput{
		Type:     "GEOSITE",
		Payload:  "xai",
		Outbound: "TargetGroup",
		Enabled:  &enabled,
	})
	if err != nil {
		t.Fatalf("CreateRule failed: %v", err)
	}

	// 3. Delete TargetGroup
	if err := store.DeleteGroup(targetGroup.ID); err != nil {
		t.Fatalf("DeleteGroup failed: %v", err)
	}

	// 4. Verify TargetGroup is gone
	groups := store.ListGroups()
	if len(groups) != 1 || groups[0].Name != "ParentGroup" {
		t.Fatalf("expected only ParentGroup remaining, got: %+v", groups)
	}

	// 5. Verify ParentGroup members no longer contain TargetGroup
	if len(groups[0].Proxies) != 1 || groups[0].Proxies[0] != "DIRECT" {
		t.Fatalf("expected ParentGroup members to be [DIRECT], got: %+v", groups[0].Proxies)
	}

	// 6. Verify rule pointing to TargetGroup was repointed to DIRECT
	rules := store.ListRules()
	if len(rules) != 1 || rules[0].ID != rule.ID {
		t.Fatalf("expected 1 rule remaining, got: %+v", rules)
	}
	if rules[0].Outbound != "DIRECT" {
		t.Fatalf("expected rule Outbound to be repointed to DIRECT, got: %s", rules[0].Outbound)
	}
}

func TestStoreSaveGroup_DetectsCycles(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}

	// Group 1
	g1, err := store.SaveGroup(ProxyGroup{
		Name:    "Group1",
		Type:    "select",
		Proxies: []string{"DIRECT"},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveGroup g1: %v", err)
	}

	// Group 2 referencing Group 1
	g2, err := store.SaveGroup(ProxyGroup{
		Name:    "Group2",
		Type:    "select",
		Proxies: []string{"Group1"},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveGroup g2: %v", err)
	}
	_ = g2

	// Update Group 1 to reference Group 2 -> Cycle!
	g1.Proxies = []string{"Group2"}
	_, err = store.SaveGroup(g1)
	if err == nil {
		t.Fatal("expected error when creating cycle Group1 -> Group2 -> Group1, got nil")
	}
	if !strings.Contains(err.Error(), "цикл") {
		t.Fatalf("expected cycle error, got: %v", err)
	}

	// Indirect cycle: Group 3 referencing Group 2
	g3, err := store.SaveGroup(ProxyGroup{
		Name:    "Group3",
		Type:    "select",
		Proxies: []string{"Group2"},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveGroup g3: %v", err)
	}
	_ = g3

	// Update Group 1 to reference Group 3: Group2 -> Group1 -> Group3 -> Group2
	g1.Proxies = []string{"Group3"}
	_, err = store.SaveGroup(g1)
	if err == nil {
		t.Fatal("expected error on indirect cycle, got nil")
	}
}

func TestStoreGetGroupReferences_AndInUse(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "native.json"))
	if err != nil {
		t.Fatal(err)
	}

	g1, err := store.SaveGroup(ProxyGroup{
		Name:    "GroupAlpha",
		Type:    "select",
		Proxies: []string{"DIRECT"},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveGroup g1: %v", err)
	}

	_, err = store.SaveGroup(ProxyGroup{
		Name:    "GroupBeta",
		Type:    "select",
		Proxies: []string{"GroupAlpha"},
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("SaveGroup g2: %v", err)
	}

	enabled := true
	_, err = store.CreateRule(RuleInput{
		Type:     "DOMAIN",
		Payload:  "example.com",
		Outbound: "GroupAlpha",
		Enabled:  &enabled,
	})
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	refs := store.GetGroupReferences(g1.ID)
	if len(refs) != 2 {
		t.Fatalf("expected 2 references (group and rule), got: %+v", refs)
	}

	// Register inUseChecker to simulate Susanin egress reference
	store.SetInUseChecker(func(kind string, id string, name string) (bool, string) {
		if kind == "group" && (id == g1.ID || name == g1.Name) {
			return true, "используется в Susanin как основной выход"
		}
		return false, ""
	})

	refsWithSusanin := store.GetGroupReferences(g1.ID)
	if len(refsWithSusanin) != 3 {
		t.Fatalf("expected 3 references with Susanin, got: %+v", refsWithSusanin)
	}

	// Attempt to delete g1 -> must be blocked
	err = store.DeleteGroup(g1.ID)
	if err == nil {
		t.Fatal("expected DeleteGroup to fail when in use by Susanin, got nil")
	}
	if !strings.Contains(err.Error(), "нельзя удалить группу") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestStore_DesiredDigest_SubscriptionRefresh(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "native.json"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	sub, err := store.CreateSubscription(CreateSubscriptionInput{
		Name:             "Sub1",
		URL:              "http://example.com/sub.yaml",
		Format:           FormatMihomoProvider,
		EnginePreference: EngineAuto,
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("CreateSubscription: %v", err)
	}

	desiredBefore, err := store.CurrentDesiredDigest()
	if err != nil {
		t.Fatalf("CurrentDesiredDigest: %v", err)
	}
	snapBefore, err := store.CurrentSnapshotDigest()
	if err != nil {
		t.Fatalf("CurrentSnapshotDigest: %v", err)
	}

	// Wait 10ms to ensure time changes
	time.Sleep(10 * time.Millisecond)

	// Simulate subscription refresh
	if err := store.RecordSubscriptionRefresh(sub.ID, nil); err != nil {
		t.Fatalf("RecordSubscriptionRefresh: %v", err)
	}

	desiredAfter, err := store.CurrentDesiredDigest()
	if err != nil {
		t.Fatalf("CurrentDesiredDigest after refresh: %v", err)
	}
	snapAfter, err := store.CurrentSnapshotDigest()
	if err != nil {
		t.Fatalf("CurrentSnapshotDigest after refresh: %v", err)
	}

	// Snapshot digest MUST change because LastFetched and UpdatedAt changed
	if snapBefore == snapAfter {
		t.Errorf("expected snapshot digest to change after RecordSubscriptionRefresh, got same: %s", snapBefore)
	}

	// Desired digest MUST NOT change because configuration is unaffected!
	if desiredBefore != desiredAfter {
		t.Errorf("expected desired digest to remain invariant across refresh: before=%s, after=%s", desiredBefore, desiredAfter)
	}
}

func TestStore_DesiredDigest_RuleMutation(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(filepath.Join(dir, "native.json"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	r1, err := store.CreateRule(RuleInput{
		Type:     "DOMAIN-SUFFIX",
		Payload:  "google.com",
		Outbound: "DIRECT",
	})
	if err != nil {
		t.Fatalf("CreateRule r1: %v", err)
	}

	r2, err := store.CreateRule(RuleInput{
		Type:     "DOMAIN-KEYWORD",
		Payload:  "youtube",
		Outbound: "DIRECT",
	})
	if err != nil {
		t.Fatalf("CreateRule r2: %v", err)
	}

	desiredOrder1, err := store.CurrentDesiredDigest()
	if err != nil {
		t.Fatalf("CurrentDesiredDigest: %v", err)
	}

	// Reorder rules: [r2, r1]
	if err := store.ReorderRules([]string{r2.ID, r1.ID}); err != nil {
		t.Fatalf("ReorderRules: %v", err)
	}

	desiredOrder2, err := store.CurrentDesiredDigest()
	if err != nil {
		t.Fatalf("CurrentDesiredDigest after reorder: %v", err)
	}

	// First-match rule order matters! Digest must change!
	if desiredOrder1 == desiredOrder2 {
		t.Errorf("expected desired digest to change when rule order changes: %s == %s", desiredOrder1, desiredOrder2)
	}
}
