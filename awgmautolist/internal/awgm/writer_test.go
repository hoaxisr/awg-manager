package awgm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/model"
)

// fakeAWGM implements the slice of the AWGM API the writer uses. State is
// guarded by mu so concurrent writer calls race on real HTTP, not on the
// fake's memory.
type fakeAWGM struct {
	mu     sync.Mutex
	srv    *httptest.Server
	t      *testing.T
	client *Client

	rules             []map[string]any // router rules, order = priority
	ruleSets          []map[string]any // rule-sets
	outbounds         []map[string]any // composite outbounds
	bindable          []map[string]any // bindable interfaces
	awgTags           []map[string]any // /awg-outbounds/tags
	tunnels           []map[string]any // /singbox/tunnels
	subs              []map[string]any // /subscriptions
	subGroups         []map[string]any // /subscriptions/groups
	applied           int              // successful staging applies
	applyErrors       int              // failed staging applies
	applyNoDraft      bool             // next apply answers NO_DRAFT
	applyCode         string           // error code for the next apply
	rulesetListErr    string           // when set, rulesets/list fails with this raw body
	dropRuleSetsAfter int              // answer rulesets/list with an empty list once this many reads happened
	failRulesDelete   bool             // rules/delete answers 500
	failRulesetsAdd   bool             // rulesets/add answers 500

	writes []string // chronology: "rules/add", "rulesets/update", ...
}

func newFakeAWGMWriter(t *testing.T) *fakeAWGM {
	t.Helper()
	f := &fakeAWGM{t: t}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/singbox/router/rules/list", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		writeData(w, f.rules)
	})
	mux.HandleFunc("/api/singbox/router/rules/add", func(w http.ResponseWriter, r *http.Request) {
		var rule map[string]any
		if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.writes = append(f.writes, "rules/add")
		f.rules = append(f.rules, rule)
		writeData(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("/api/singbox/router/rules/update", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Index int            `json:"index"`
			Rule  map[string]any `json:"rule"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if body.Index < 0 || body.Index >= len(f.rules) {
			http.Error(w, "rule index out of range", http.StatusBadRequest)
			return
		}
		f.writes = append(f.writes, "rules/update")
		f.rules[body.Index] = body.Rule
		writeData(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("/api/singbox/router/rules/delete", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Index int `json:"index"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.failRulesDelete {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": true, "message": "delete failed", "code": "INTERNAL"})
			return
		}
		if body.Index < 0 || body.Index >= len(f.rules) {
			http.Error(w, "rule index out of range", http.StatusBadRequest)
			return
		}
		f.writes = append(f.writes, "rules/delete")
		f.rules = append(f.rules[:body.Index], f.rules[body.Index+1:]...)
		writeData(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("/api/singbox/router/rulesets/list", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.rulesetListErr != "" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(f.rulesetListErr))
			return
		}
		data := any(f.ruleSets)
		if f.dropRuleSetsAfter > 0 {
			f.dropRuleSetsAfter--
			if f.dropRuleSetsAfter == 0 {
				data = []map[string]any{}
			}
		}
		writeData(w, data)
	})
	mux.HandleFunc("/api/singbox/router/rulesets/add", func(w http.ResponseWriter, r *http.Request) {
		var rs map[string]any
		if err := json.NewDecoder(r.Body).Decode(&rs); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.failRulesetsAdd {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": true, "message": "add failed", "code": "INTERNAL"})
			return
		}
		f.writes = append(f.writes, "rulesets/add")
		f.ruleSets = append(f.ruleSets, rs)
		writeData(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("/api/singbox/router/rulesets/update", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tag     string         `json:"tag"`
			RuleSet map[string]any `json:"ruleSet"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.writes = append(f.writes, "rulesets/update")
		replaced := false
		for i, rs := range f.ruleSets {
			if rs["tag"] == body.Tag {
				f.ruleSets[i] = body.RuleSet
				replaced = true
				break
			}
		}
		if !replaced {
			f.ruleSets = append(f.ruleSets, body.RuleSet)
		}
		writeData(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("/api/singbox/router/rulesets/delete", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tag string `json:"tag"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.writes = append(f.writes, "rulesets/delete")
		kept := f.ruleSets[:0]
		for _, rs := range f.ruleSets {
			if rs["tag"] != body.Tag {
				kept = append(kept, rs)
			}
		}
		f.ruleSets = kept
		writeData(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("/api/singbox/router/outbounds/list", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		writeData(w, f.outbounds)
	})
	mux.HandleFunc("/api/singbox/router/outbounds/add", func(w http.ResponseWriter, r *http.Request) {
		var ob map[string]any
		if err := json.NewDecoder(r.Body).Decode(&ob); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.writes = append(f.writes, "outbounds/add")
		f.outbounds = append(f.outbounds, ob)
		writeData(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("/api/singbox/router/outbounds/update", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Tag      string         `json:"tag"`
			Outbound map[string]any `json:"outbound"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.writes = append(f.writes, "outbounds/update")
		for i, ob := range f.outbounds {
			if ob["tag"] == body.Tag {
				f.outbounds[i] = body.Outbound
			}
		}
		writeData(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("/api/singbox/router/bindable-interfaces", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		writeData(w, f.bindable)
	})
	mux.HandleFunc("/api/singbox/awg-outbounds/tags", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		writeData(w, f.awgTags)
	})
	mux.HandleFunc("/api/singbox/tunnels", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		writeData(w, f.tunnels)
	})
	mux.HandleFunc("/api/singbox/subscriptions", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		writeData(w, f.subs)
	})
	mux.HandleFunc("/api/singbox/subscriptions/groups", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		writeData(w, f.subGroups)
	})
	mux.HandleFunc("/api/singbox/router/staging/apply", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.writes = append(f.writes, "staging/apply")
		if f.applyNoDraft || f.applyCode != "" {
			code := f.applyCode
			if code == "" {
				code = "NO_DRAFT"
			}
			f.applyNoDraft = false
			f.applyCode = ""
			f.applyErrors++
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": true, "message": "apply rejected", "code": code})
			return
		}
		f.applied++
		writeData(w, map[string]bool{"ok": true})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	client, err := NewClient(f.srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	f.client = client
	return f
}

// writeData answers with the AWGM success envelope. Nil slices are
// encoded as [] like the real server, never null.
func writeData(w http.ResponseWriter, data any) {
	if list, ok := data.([]map[string]any); ok && list == nil {
		data = []map[string]any{}
	}
	if list, ok := data.([]any); ok && list == nil {
		data = []any{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data})
}

func cloneMaps(src []map[string]any) []map[string]any {
	if src == nil {
		return nil
	}
	out := make([]map[string]any, len(src))
	for i, m := range src {
		raw, _ := json.Marshal(m)
		_ = json.Unmarshal(raw, &out[i])
	}
	return out
}

func (f *fakeAWGM) writesSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.writes...)
}

func (f *fakeAWGM) appliedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.applied
}

// ownedObject returns the writer's unconditional rule-set object for key.
func (f *fakeAWGM) ownedObject(t *testing.T, key string) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, rs := range f.ruleSets {
		if rs["tag"] != ruleSetTag {
			continue
		}
		rules, _ := rs["rules"].([]any)
		for _, rawRule := range rules {
			rule, ok := rawRule.(map[string]any)
			if !ok || len(rule) != 1 {
				continue
			}
			if _, ok := rule[key].([]any); ok {
				return rule
			}
		}
	}
	t.Fatalf("no owned %s object in %+v", key, f.ruleSets)
	return nil
}

// destinations returns every destination in the owned object of key.
func (f *fakeAWGM) destinations(t *testing.T, key string) []string {
	t.Helper()
	rule := f.ownedObject(t, key)
	list, _ := rule[key].([]any)
	out := make([]string, 0, len(list))
	for _, entry := range list {
		out = append(out, fmt.Sprint(entry))
	}
	return out
}

func domainTarget(domain string) model.Target {
	t, err := model.NewTarget(domain, "", model.NetworkTCP, 443)
	if err != nil {
		panic(err)
	}
	return t
}

func ipTarget(ip string) model.Target {
	t, err := model.NewTarget("", ip, model.NetworkUDP, 53)
	if err != nil {
		panic(err)
	}
	return t
}

// healthy bindable interface setup: br0 bindable, nothing else mapped.
func bindableSetup(f *fakeAWGM) {
	f.bindable = []map[string]any{{"name": "br0"}, {"name": "eth3"}}
}

func TestWriterAddDomainIPv4AndIPv6(t *testing.T) {
	f := newFakeAWGMWriter(t)
	bindableSetup(f)
	w := NewWriter(f.client)

	res, err := w.Add(context.Background(), domainTarget("www.example.co.uk"), "br0")
	if err != nil {
		t.Fatal(err)
	}
	if res.Outbound != targetOutboundTag || !res.Persisted {
		t.Fatalf("result=%+v", res)
	}
	suffixes := f.destinations(t, "domain_suffix")
	if len(suffixes) != 1 || suffixes[0] != "www.example.co.uk" {
		t.Fatalf("domain_suffix=%v, want the registered zone", suffixes)
	}
	if len(f.rules) != 1 || f.rules[0]["outbound"] != targetOutboundTag {
		t.Fatalf("owner rule=%+v", f.rules)
	}
	if !reflect.DeepEqual(f.rules[0]["rule_set"], []any{ruleSetTag}) {
		t.Fatalf("owner rule_set=%+v", f.rules[0]["rule_set"])
	}
	if f.appliedCount() != 1 {
		t.Fatalf("applied=%d", f.appliedCount())
	}
	// Target outbound is the writer's own direct outbound.
	if len(f.outbounds) != 1 || f.outbounds[0]["bind_interface"] != "br0" || f.outbounds[0]["type"] != "direct" {
		t.Fatalf("outbounds=%+v", f.outbounds)
	}

	// IPv6 IP-only target: /128, whole address.
	res, err = w.Add(context.Background(), ipTarget("2001:db8::1"), "br0")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Persisted {
		t.Fatalf("ipv6 result=%+v", res)
	}
	cidrs := f.destinations(t, "ip_cidr")
	if len(cidrs) != 1 || cidrs[0] != "2001:db8::1/128" {
		t.Fatalf("ip_cidr=%v, want [2001:db8::1/128]", cidrs)
	}

	// IPv4 IP-only target: /32.
	if _, err := w.Add(context.Background(), ipTarget("203.0.113.9"), "br0"); err != nil {
		t.Fatal(err)
	}
	cidrs = f.destinations(t, "ip_cidr")
	if len(cidrs) != 2 || cidrs[1] != "203.0.113.9/32" {
		t.Fatalf("ip_cidr=%v", cidrs)
	}
	if f.appliedCount() != 3 {
		t.Fatalf("applies=%d, want 3", f.appliedCount())
	}
}

func TestWriterSecondPortSameIPNoOp(t *testing.T) {
	f := newFakeAWGMWriter(t)
	bindableSetup(f)
	w := NewWriter(f.client)

	first, err := w.Add(context.Background(), model.Target{IP: "203.0.113.9", Network: model.NetworkTCP, Port: 443}, "br0")
	if err != nil {
		t.Fatal(err)
	}
	if !first.Persisted {
		t.Fatalf("first=%+v", first)
	}
	writes := f.writesSnapshot()

	// Same address, different port and protocol: same destination, no write.
	second, err := w.Add(context.Background(), model.Target{IP: "203.0.113.9", Network: model.NetworkUDP, Port: 53}, "br0")
	if err != nil {
		t.Fatal(err)
	}
	if second.Persisted || second.Outbound != targetOutboundTag {
		t.Fatalf("second=%+v", second)
	}
	if !reflect.DeepEqual(f.writesSnapshot(), writes) {
		t.Fatalf("duplicate wrote again: %v", f.writesSnapshot())
	}
	if f.appliedCount() != 1 {
		t.Fatalf("applies=%d, want 1", f.appliedCount())
	}
}

func TestWriterPreservesForeignAndConditionalRules(t *testing.T) {
	f := newFakeAWGMWriter(t)
	bindableSetup(f)
	conditional := map[string]any{
		"domain_suffix": []any{"old.example"}, "network": "udp",
		"source_ip_cidr": []any{"192.168.1.50/32"}, "comment": "user data",
	}
	exact := map[string]any{"domain": []any{"exact.example"}, "ip_cidr": []any{"198.51.100.5/32"}}
	regex := map[string]any{"domain_regex": []any{`^literal\.example$`}}
	f.ruleSets = []map[string]any{{
		"tag": ruleSetTag, "type": "inline",
		"rules": []any{conditional, exact, regex},
	}}
	narrowed := map[string]any{
		"rule_set": []any{ruleSetTag}, "action": "route", "outbound": "legacy-target",
		"source_ip_cidr": []any{"192.168.1.50/32"}, "network": "tcp", "comment": "keep",
	}
	shared := map[string]any{
		"rule_set": []any{ruleSetTag, "user-set"}, "action": "route", "outbound": "user-target",
	}
	managed := map[string]any{
		"rule_set": []any{ruleSetTag}, "action": "route", "outbound": "vpn", "awgm_managed": "selective-ip",
	}
	f.rules = []map[string]any{narrowed, shared, managed}

	w := NewWriter(f.client)
	if _, err := w.Add(context.Background(), domainTarget("new.example"), "br0"); err != nil {
		t.Fatal(err)
	}

	rules := f.ruleSets[0]["rules"].([]any)
	if len(rules) != 4 {
		t.Fatalf("ruleset rules=%+v", rules)
	}
	if !reflect.DeepEqual(rules[0], conditional) || !reflect.DeepEqual(rules[1], exact) || !reflect.DeepEqual(rules[2], regex) {
		t.Fatalf("foreign objects changed: %+v", rules[:3])
	}
	if !reflect.DeepEqual(rules[3], map[string]any{"domain_suffix": []any{"new.example"}}) {
		t.Fatalf("new object=%+v", rules[3])
	}
	if len(f.rules) != 4 {
		t.Fatalf("router rules=%+v", f.rules)
	}
	if !reflect.DeepEqual(f.rules[0], narrowed) || !reflect.DeepEqual(f.rules[1], shared) || !reflect.DeepEqual(f.rules[2], managed) {
		t.Fatalf("foreign router rules changed: %+v", f.rules[:3])
	}
	if f.rules[3]["outbound"] != targetOutboundTag || len(f.rules[3]) != 3 {
		t.Fatalf("created owner=%+v", f.rules[3])
	}
}

func TestWriterOwnerPreflightBeforeMutation(t *testing.T) {
	f := newFakeAWGMWriter(t)
	bindableSetup(f)
	ownerA := map[string]any{"rule_set": []any{ruleSetTag}, "action": "route", "outbound": "a"}
	ownerB := map[string]any{"rule_set": []any{ruleSetTag}, "action": "route", "outbound": "b"}
	f.rules = []map[string]any{ownerA, ownerB}

	w := NewWriter(f.client)
	_, err := w.Add(context.Background(), domainTarget("x.example"), "br0")
	if err == nil || err.Error() != "preflight: 2 unconditional awgm-autolist owner rules; refusing to pick one" {
		t.Fatalf("err=%v", err)
	}
	if writes := f.writesSnapshot(); len(writes) != 0 {
		t.Fatalf("preflight failure mutated: %v", writes)
	}
	_, err = w.Remove(context.Background(), domainTarget("x.example"), "br0")
	if err == nil || err.Error() != "preflight: 2 unconditional awgm-autolist owner rules; refusing to pick one" {
		t.Fatalf("remove err=%v", err)
	}
	if writes := f.writesSnapshot(); len(writes) != 0 {
		t.Fatalf("remove preflight mutated: %v", writes)
	}
}

func TestWriterInterfaceMapping(t *testing.T) {
	type catalog struct {
		bindable  []map[string]any
		awgTags   []map[string]any
		tunnels   []map[string]any
		subs      []map[string]any
		subGroups []map[string]any
	}
	tests := []struct {
		name         string
		iface        string
		cat          catalog
		wantOutbound string
		wantMutated  bool // expect an own outbound write
		wantErr      string
	}{
		{
			name:         "bindable creates own outbound",
			iface:        "eth3",
			cat:          catalog{bindable: []map[string]any{{"name": "eth3"}}},
			wantOutbound: targetOutboundTag,
			wantMutated:  true,
		},
		{
			name:  "managed AWG uses catalog tag",
			iface: "nwg7",
			cat: catalog{
				awgTags: []map[string]any{
					{"tag": "decoy", "iface": "nwg70"},
					{"tag": "awg-managed-7", "iface": "nwg7"},
				},
			},
			wantOutbound: "awg-managed-7",
		},
		{
			name:  "standalone sing-box tunnel",
			iface: "t2s1",
			cat: catalog{
				tunnels: []map[string]any{
					{"tag": "decoy", "kernelInterface": "t2s10", "running": true},
					{"tag": "standalone-node", "kernelInterface": "t2s1", "running": true},
				},
			},
			wantOutbound: "standalone-node",
		},
		{
			name:  "subscription selector",
			iface: "t2s2",
			cat: catalog{
				subs: []map[string]any{
					{"selectorTag": "off", "proxyIndex": 2, "enabled": false},
					{"selectorTag": "sub-2", "proxyIndex": 2, "enabled": true},
				},
			},
			wantOutbound: "sub-2",
		},
		{
			name:  "subscription group",
			iface: "t2s4",
			cat: catalog{
				subGroups: []map[string]any{
					{"tag": "off-group", "proxyIndex": 4, "enabled": false},
					{"tag": "group-4", "proxyIndex": 4, "enabled": true},
				},
			},
			wantOutbound: "group-4",
		},
		{
			name:    "no mapping",
			iface:   "t2s9",
			cat:     catalog{},
			wantErr: "no exact managed outbound",
		},
		{
			name:  "ambiguous sing-box",
			iface: "t2s3",
			cat: catalog{
				tunnels: []map[string]any{
					{"tag": "a", "kernelInterface": "t2s3", "running": true},
					{"tag": "b", "kernelInterface": "t2s3", "running": true},
				},
			},
			wantErr: "multiple exact outbounds",
		},
		{
			name:  "disabled subscription",
			iface: "t2s5",
			cat: catalog{
				subs: []map[string]any{{"selectorTag": "off", "proxyIndex": 5, "enabled": false}},
			},
			wantErr: "disabled or unmapped",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeAWGMWriter(t)
			f.bindable = tc.cat.bindable
			f.awgTags = tc.cat.awgTags
			f.tunnels = tc.cat.tunnels
			f.subs = tc.cat.subs
			f.subGroups = tc.cat.subGroups
			w := NewWriter(f.client)

			res, err := w.Add(context.Background(), domainTarget("m.example"), tc.iface)
			if tc.wantErr != "" {
				if err == nil || !contains(err.Error(), tc.wantErr) {
					t.Fatalf("err=%v, want %q", err, tc.wantErr)
				}
				if writes := f.writesSnapshot(); len(writes) != 0 {
					t.Fatalf("mapping failure wrote: %v", writes)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if res.Outbound != tc.wantOutbound {
				t.Fatalf("outbound=%q, want %q", res.Outbound, tc.wantOutbound)
			}
			outboundWrites := 0
			for _, wr := range f.writesSnapshot() {
				if wr == "outbounds/add" || wr == "outbounds/update" {
					outboundWrites++
				}
			}
			if tc.wantMutated && outboundWrites == 0 {
				t.Fatalf("expected an own outbound write, got %v", f.writesSnapshot())
			}
			if !tc.wantMutated && outboundWrites != 0 {
				t.Fatalf("catalog mapping wrote an outbound: %v", f.writesSnapshot())
			}
			if len(f.rules) != 1 || f.rules[0]["outbound"] != tc.wantOutbound {
				t.Fatalf("owner=%+v", f.rules)
			}
		})
	}
}

func TestWriterRemoveTouchesOnlyOwned(t *testing.T) {
	f := newFakeAWGMWriter(t)
	bindableSetup(f)
	conditional := map[string]any{"domain_suffix": []any{"gone.example"}, "network": "udp"}
	other := map[string]any{"domain_suffix": []any{"stay.example"}, "comment": "keep"}
	f.ruleSets = []map[string]any{{
		"tag": ruleSetTag, "type": "inline",
		"rules": []any{conditional, other, map[string]any{"domain_suffix": []any{"gone.example", "kept-too.example"}}},
	}}
	narrowed := map[string]any{
		"rule_set": []any{ruleSetTag}, "action": "route", "outbound": "legacy",
		"source_ip_cidr": []any{"192.168.1.1/32"},
	}
	managed := map[string]any{"rule_set": []any{ruleSetTag}, "action": "route", "outbound": "vpn", "awgm_managed": "x"}
	f.rules = []map[string]any{narrowed, managed}

	w := NewWriter(f.client)
	res, err := w.Remove(context.Background(), domainTarget("gone.example"), "br0")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Persisted {
		t.Fatalf("remove result=%+v", res)
	}
	rules := f.ruleSets[0]["rules"].([]any)
	if len(rules) != 3 {
		t.Fatalf("rules=%+v", rules)
	}
	// The conditional object is not owned: untouched.
	if !reflect.DeepEqual(rules[0], conditional) {
		t.Fatalf("conditional object changed: %+v", rules[0])
	}
	if !reflect.DeepEqual(rules[1], other) {
		t.Fatalf("other object changed: %+v", rules[1])
	}
	// The owned object keeps its other destination.
	if !reflect.DeepEqual(rules[2], map[string]any{"domain_suffix": []any{"kept-too.example"}}) {
		t.Fatalf("multi-destination object=%+v", rules[2])
	}
	// Narrowed and managed router rules survive.
	if len(f.rules) != 2 || !reflect.DeepEqual(f.rules[0], narrowed) || !reflect.DeepEqual(f.rules[1], managed) {
		t.Fatalf("router rules=%+v", f.rules)
	}
}

func TestWriterRemoveDeletesRuleSetAndOwnerWhenEmpty(t *testing.T) {
	f := newFakeAWGMWriter(t)
	bindableSetup(f)
	f.ruleSets = []map[string]any{{
		"tag": ruleSetTag, "type": "inline",
		"rules": []any{map[string]any{"ip_cidr": []any{"203.0.113.9/32"}}},
	}}
	f.rules = []map[string]any{{
		"rule_set": []any{ruleSetTag}, "action": "route", "outbound": targetOutboundTag,
	}}
	f.outbounds = []map[string]any{{
		"tag": targetOutboundTag, "type": "direct", "bind_interface": "br0",
	}}

	w := NewWriter(f.client)
	res, err := w.Remove(context.Background(), ipTarget("203.0.113.9"), "br0")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Persisted || res.Outbound != targetOutboundTag {
		t.Fatalf("result=%+v", res)
	}
	if len(f.ruleSets) != 0 {
		t.Fatalf("rule-sets=%+v", f.ruleSets)
	}
	if len(f.rules) != 0 {
		t.Fatalf("rules=%+v", f.rules)
	}
	// The target outbound is not removed by Remove.
	if len(f.outbounds) != 1 {
		t.Fatalf("outbounds=%+v", f.outbounds)
	}
	if f.appliedCount() != 1 {
		t.Fatalf("applies=%d", f.appliedCount())
	}
}

func TestWriterRemoveNoOpWhenAbsent(t *testing.T) {
	f := newFakeAWGMWriter(t)
	bindableSetup(f)
	w := NewWriter(f.client)
	res, err := w.Remove(context.Background(), domainTarget("absent.example"), "br0")
	if err != nil {
		t.Fatal(err)
	}
	if res.Persisted || res.Outbound != "" {
		t.Fatalf("result=%+v", res)
	}
	if writes := f.writesSnapshot(); len(writes) != 0 {
		t.Fatalf("absent removal wrote: %v", writes)
	}
}

func TestWriterHealthyRepeatNoOp(t *testing.T) {
	f := newFakeAWGMWriter(t)
	bindableSetup(f)
	w := NewWriter(f.client)
	if _, err := w.Add(context.Background(), domainTarget("repeat.example"), "br0"); err != nil {
		t.Fatal(err)
	}
	writes := f.writesSnapshot()
	applied := f.appliedCount()

	res, err := w.Add(context.Background(), domainTarget("repeat.example"), "br0")
	if err != nil {
		t.Fatal(err)
	}
	if res.Persisted {
		t.Fatalf("repeat=%+v", res)
	}
	if !reflect.DeepEqual(f.writesSnapshot(), writes) || f.appliedCount() != applied {
		t.Fatalf("healthy repeat wrote: %v", f.writesSnapshot())
	}
}

func TestWriterApplyFailureKeepsPartialPersisted(t *testing.T) {
	f := newFakeAWGMWriter(t)
	bindableSetup(f)
	w := NewWriter(f.client)

	// Rule-set and owner are written, then apply fails.
	f.applyCode = "VALIDATION"
	res, err := w.Add(context.Background(), domainTarget("fail.example"), "br0")
	if err == nil || !contains(err.Error(), "staging apply failed") {
		t.Fatalf("err=%v", err)
	}
	if !res.Persisted || res.Outbound != targetOutboundTag {
		t.Fatalf("partial result=%+v", res)
	}
	if f.appliedCount() != 0 {
		t.Fatalf("applies=%d", f.appliedCount())
	}

	// A repeat now sees the state present but unapplied: no new writes, and
	// the readback still proves the expected router state.
	res, err = w.Add(context.Background(), domainTarget("fail.example"), "br0")
	if err != nil {
		t.Fatal(err)
	}
	if res.Persisted {
		t.Fatalf("repeat after failure=%+v", res)
	}
}

func TestWriterNO_DraftGenuineAndFalse(t *testing.T) {
	// Genuine NO_DRAFT: the apply endpoint has nothing to apply (the inline
	// ruleset update already hot-reloaded), and the readback proves the
	// expected state — success.
	f := newFakeAWGMWriter(t)
	bindableSetup(f)
	f.applyNoDraft = true
	w := NewWriter(f.client)
	res, err := w.Add(context.Background(), domainTarget("nodraft.example"), "br0")
	if err != nil {
		t.Fatalf("genuine NO_DRAFT: %v", err)
	}
	if !res.Persisted {
		t.Fatalf("result=%+v", res)
	}
	if f.appliedCount() != 0 {
		t.Fatalf("applies=%d", f.appliedCount())
	}

	// False NO_DRAFT: the mutations never landed, so the readback must fail
	// closed instead of reporting success.
	f2 := newFakeAWGMWriter(t)
	bindableSetup(f2)
	f2.failRulesetsAdd = true
	w2 := NewWriter(f2.client)
	_, err = w2.Add(context.Background(), domainTarget("nodraft.example"), "br0")
	if err == nil || !contains(err.Error(), "ruleset create") {
		t.Fatalf("err=%v", err)
	}
}

func TestWriterReadbackFailsWhenStateVanished(t *testing.T) {
	// A concurrent editor erases the rule-set after our mutation landed:
	// apply succeeded, but the readback observes a vanished object, so the
	// call must fail instead of reporting success.
	f := newFakeAWGMWriter(t)
	bindableSetup(f)
	// rules/list is read three times per Add (preflight, owner ensure,
	// readback) and rulesets/list twice (mutation, readback); the drop
	// counter erases the set after the mutation-path rulesets/list read.
	f.mu.Lock()
	f.dropRuleSetsAfter = 2
	f.mu.Unlock()
	w := NewWriter(f.client)
	res, err := w.Add(context.Background(), domainTarget("vanish.example"), "br0")
	if err == nil || !contains(err.Error(), "readback") {
		t.Fatalf("err=%v", err)
	}
	// The objects were written before they vanished: Persisted stays honest.
	if !res.Persisted {
		t.Fatalf("result=%+v", res)
	}
}

func TestWriterRawAPIErrorsSurface(t *testing.T) {
	raw := `{"error":true,"message":"` + "catalog failure " + `","code":"INTERNAL"}`
	tests := []struct {
		name string
		set  func(*fakeAWGM)
	}{
		{"ruleset list", func(f *fakeAWGM) { f.rulesetListErr = raw }},
		{"rules delete", func(f *fakeAWGM) { f.failRulesDelete = true }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeAWGMWriter(t)
			bindableSetup(f)
			tc.set(f)
			w := NewWriter(f.client)
			_, err := w.Remove(context.Background(), domainTarget("gone.example"), "br0")
			if err != nil {
				var apiErr *APIError
				if !errors.As(err, &apiErr) {
					t.Fatalf("err=%v, want raw APIError", err)
				}
			}
		})
	}
}

func TestWriterConcurrentAddsAllPersist(t *testing.T) {
	f := newFakeAWGMWriter(t)
	bindableSetup(f)
	w := NewWriter(f.client)

	const n = 8
	domains := make([]string, n)
	for i := range domains {
		domains[i] = fmt.Sprintf("parallel-%d.example", i)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]WriteResult, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = w.Add(context.Background(), domainTarget(domains[i]), "br0")
		}(i)
	}
	close(start)
	wg.Wait()
	for i := range errs {
		if errs[i] != nil {
			t.Fatalf("add %d: %v", i, errs[i])
		}
		if !results[i].Persisted {
			t.Fatalf("add %d: duplicate reported Persisted=false: %+v", i, results[i])
		}
	}
	got := f.destinations(t, "domain_suffix")
	if len(got) != n {
		t.Fatalf("destinations=%v", got)
	}
	seen := map[string]bool{}
	for _, d := range got {
		seen[d] = true
	}
	for _, d := range domains {
		if !seen[d] {
			t.Fatalf("destination %q lost: %v", d, got)
		}
	}
	if len(f.rules) != 1 {
		t.Fatalf("owner rules=%+v", f.rules)
	}
}

func TestWriterInvalidTargetAndInterfaceFailClosed(t *testing.T) {
	f := newFakeAWGMWriter(t)
	bindableSetup(f)
	w := NewWriter(f.client)
	empty := model.Target{Network: model.NetworkTCP, Port: 443}
	if _, err := w.Add(context.Background(), empty, "br0"); err == nil {
		t.Fatal("empty target accepted")
	}
	if _, err := w.Add(context.Background(), domainTarget("x.example"), ""); err == nil {
		t.Fatal("empty interface accepted")
	}
	if _, err := w.Add(context.Background(), domainTarget("x.example"), "br0 eth0"); err == nil {
		t.Fatal("interface with a space accepted")
	}
	if writes := f.writesSnapshot(); len(writes) != 0 {
		t.Fatalf("validation failures wrote: %v", writes)
	}
}

func TestWriterContextCancelled(t *testing.T) {
	f := newFakeAWGMWriter(t)
	bindableSetup(f)
	w := NewWriter(f.client)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := w.Add(ctx, domainTarget("ctx.example"), "br0"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if writes := f.writesSnapshot(); len(writes) != 0 {
		t.Fatalf("cancelled call wrote: %v", writes)
	}
}

func TestWriterNeverSubstitutesArbitraryGroup(t *testing.T) {
	// A proxy group exists but has no exact interface mapping: the writer
	// must fail, not pick the group anyway.
	f := newFakeAWGMWriter(t)
	f.bindable = []map[string]any{}
	f.tunnels = []map[string]any{{"tag": "some-group", "kernelInterface": "other0", "running": true}}
	w := NewWriter(f.client)
	_, err := w.Add(context.Background(), domainTarget("g.example"), "t2s0")
	if err == nil || !contains(err.Error(), "no exact managed outbound") {
		t.Fatalf("err=%v", err)
	}
	if writes := f.writesSnapshot(); len(writes) != 0 {
		t.Fatalf("group substitution wrote: %v", writes)
	}
}

func TestWriterRemoveOnBindableDoesNotRewriteOutbound(t *testing.T) {
	// Remove must not create or update the target outbound.
	f := newFakeAWGMWriter(t)
	f.bindable = []map[string]any{{"name": "br0"}}
	w := NewWriter(f.client)
	res, err := w.Remove(context.Background(), domainTarget("absent.example"), "br0")
	if err != nil {
		t.Fatal(err)
	}
	if res.Outbound != "" || res.Persisted {
		t.Fatalf("result=%+v", res)
	}
	for _, wr := range f.writesSnapshot() {
		if wr == "outbounds/add" || wr == "outbounds/update" {
			t.Fatalf("remove rewrote the outbound: %v", f.writesSnapshot())
		}
	}
}

func TestWriterOwnerRepairIsNotANewEntry(t *testing.T) {
	f := newFakeAWGMWriter(t)
	bindableSetup(f)
	w := NewWriter(f.client)
	target := domainTarget("repair.example")
	if _, err := w.Add(context.Background(), target, "br0"); err != nil {
		t.Fatal(err)
	}
	for _, rule := range f.rules {
		if isOwnedRuleRaw(rule) {
			rule["outbound"] = "direct"
		}
	}
	res, err := w.Add(context.Background(), target, "br0")
	if err != nil || !res.Persisted || res.NewEntry {
		t.Fatalf("owner repair counted as new destination: result=%+v err=%v", res, err)
	}
}

func TestWriterRemoveAfterTunnelDisappears(t *testing.T) {
	f := newFakeAWGMWriter(t)
	f.bindable = []map[string]any{}
	f.tunnels = []map[string]any{{"tag": "vpn", "kernelInterface": "t2s1", "running": true}}
	w := NewWriter(f.client)
	if _, err := w.Add(context.Background(), domainTarget("gone.example"), "t2s1"); err != nil {
		t.Fatal(err)
	}
	// The tunnel is completely gone when the user removes the entry. The
	// existing owner outbound is sufficient; no catalog lookup or repair is needed.
	f.tunnels = nil
	res, err := w.Remove(context.Background(), domainTarget("gone.example"), "t2s1")
	if err != nil || !res.Persisted || res.NewEntry || res.Outbound != "vpn" {
		t.Fatalf("remove after tunnel deletion: result=%+v err=%v", res, err)
	}
	if len(f.rules) != 0 || len(f.ruleSets) != 0 {
		t.Fatalf("owned empty objects were not cleaned: rules=%v sets=%v", f.rules, f.ruleSets)
	}
}

// contains is a tiny substring helper avoiding strings import churn.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
