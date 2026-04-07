package connections

import (
	"bytes"
	"context"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/dnsroute"
)

// fakeLister implements DNSListLister for tests.
type fakeLister struct {
	lists []dnsroute.DomainList
	err   error
}

func (f *fakeLister) List(_ context.Context) ([]dnsroute.DomainList, error) {
	return f.lists, f.err
}

func TestParseGroupNameToListID(t *testing.T) {
	tests := []struct {
		name string
		want string
		ok   bool
	}{
		{"AWG_6_youtube_1", "list_6", true},
		{"AWG_1_youtube_1", "list_1", true},
		{"AWG_2_instagram_facebook_w_1", "list_2", true},
		{"AWG_42_my_long_list_5", "list_42", true},
		{"AWG_1_a_1", "list_1", true},
		{"NOT_AWG_1_x_1", "", false},
		{"AWG", "", false},
		{"AWG_", "", false},
		{"AWG__no_num_1", "", false},
		{"AWG_abc_x_1", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseGroupNameToListID(tt.name)
			if ok != tt.ok {
				t.Fatalf("parseGroupNameToListID(%q) ok = %v, want %v", tt.name, ok, tt.ok)
			}
			if got != tt.want {
				t.Errorf("parseGroupNameToListID(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestParseObjectGroupRuntime_SingleEntry(t *testing.T) {
	body := []byte(`{
		"group": [
			{
				"group-name": "AWG_1_youtube_1",
				"entry": [
					{
						"fqdn": "m.youtube.com",
						"type": "runtime",
						"parent": "youtube.com",
						"ipv4": [
							{"address": "142.251.1.100"},
							{"address": "142.251.1.101"}
						],
						"ipv6": []
					}
				]
			}
		]
	}`)

	groups, err := parseObjectGroupRuntime(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 1 {
		t.Fatalf("groups = %d, want 1", len(groups))
	}
	g := groups[0]
	if g.Name != "AWG_1_youtube_1" {
		t.Errorf("Name = %q, want AWG_1_youtube_1", g.Name)
	}
	if len(g.Entries) != 1 {
		t.Fatalf("Entries = %d, want 1", len(g.Entries))
	}
	e := g.Entries[0]
	if e.FQDN != "m.youtube.com" {
		t.Errorf("FQDN = %q, want m.youtube.com", e.FQDN)
	}
	if e.Parent != "youtube.com" {
		t.Errorf("Parent = %q, want youtube.com", e.Parent)
	}
	if len(e.IPs) != 2 {
		t.Errorf("IPs = %v, want 2", e.IPs)
	}
	if e.IPs[0] != "142.251.1.100" || e.IPs[1] != "142.251.1.101" {
		t.Errorf("IPs = %v, want [142.251.1.100, 142.251.1.101]", e.IPs)
	}
}

func TestParseObjectGroupRuntime_IPv4AndIPv6(t *testing.T) {
	body := []byte(`{
		"group": [
			{
				"group-name": "AWG_1_youtube_1",
				"entry": [
					{
						"fqdn": "yt3.ggpht.com",
						"type": "runtime",
						"parent": "ggpht.com",
						"ipv4": [{"address": "64.233.161.132"}],
						"ipv6": [{"address": "2a00:1450:4010:c02::84"}]
					}
				]
			}
		]
	}`)

	groups, err := parseObjectGroupRuntime(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 1 || len(groups[0].Entries) != 1 {
		t.Fatalf("unexpected shape: %+v", groups)
	}
	ips := groups[0].Entries[0].IPs
	if len(ips) != 2 {
		t.Fatalf("IPs = %v, want 2 (one v4, one v6)", ips)
	}
	hasV4 := false
	hasV6 := false
	for _, ip := range ips {
		if ip == "64.233.161.132" {
			hasV4 = true
		}
		if ip == "2a00:1450:4010:c02::84" {
			hasV6 = true
		}
	}
	if !hasV4 || !hasV6 {
		t.Errorf("missing v4 or v6 in IPs = %v", ips)
	}
}

func TestParseObjectGroupRuntime_MultipleGroups(t *testing.T) {
	body := []byte(`{
		"group": [
			{
				"group-name": "AWG_1_youtube_1",
				"entry": [
					{"fqdn": "a.com", "parent": "a.com", "ipv4": [{"address": "1.1.1.1"}]}
				]
			},
			{
				"group-name": "AWG_2_other_1",
				"entry": [
					{"fqdn": "b.com", "parent": "b.com", "ipv4": [{"address": "2.2.2.2"}]}
				]
			}
		]
	}`)

	groups, err := parseObjectGroupRuntime(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 2 {
		t.Fatalf("groups = %d, want 2", len(groups))
	}
}

func TestParseObjectGroupRuntime_EmptyResponse(t *testing.T) {
	groups, err := parseObjectGroupRuntime(bytes.NewReader([]byte(`{"group": []}`)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(groups) != 0 {
		t.Errorf("groups = %d, want 0", len(groups))
	}
}

func TestParseObjectGroupRuntime_MalformedJSON(t *testing.T) {
	_, err := parseObjectGroupRuntime(bytes.NewReader([]byte(`not json`)))
	if err == nil {
		t.Error("expected JSON parse error")
	}
}

func TestBuildIPRuleMap_SingleHit(t *testing.T) {
	groups := []runtimeGroup{
		{
			Name: "AWG_6_youtube_1",
			Entries: []runtimeEntry{
				{
					FQDN:   "m.youtube.com",
					Parent: "youtube.com",
					IPs:    []string{"142.251.1.100"},
				},
			},
		},
	}
	lister := &fakeLister{lists: []dnsroute.DomainList{
		{ID: "list_6", Name: "YouTube"},
	}}

	m := buildIPRuleMap(context.Background(), groups, lister)

	hits := m["142.251.1.100"]
	if len(hits) != 1 {
		t.Fatalf("hits for 142.251.1.100 = %d, want 1", len(hits))
	}
	if hits[0].ListID != "list_6" {
		t.Errorf("ListID = %q, want list_6", hits[0].ListID)
	}
	if hits[0].ListName != "YouTube" {
		t.Errorf("ListName = %q, want YouTube", hits[0].ListName)
	}
	if hits[0].FQDN != "m.youtube.com" {
		t.Errorf("FQDN = %q, want m.youtube.com", hits[0].FQDN)
	}
	if hits[0].Pattern != "youtube.com" {
		t.Errorf("Pattern = %q, want youtube.com", hits[0].Pattern)
	}
}

func TestBuildIPRuleMap_IPInMultipleRules(t *testing.T) {
	// Same IP appears under two different lists (CDN shared by multiple sites)
	groups := []runtimeGroup{
		{
			Name: "AWG_6_youtube_1",
			Entries: []runtimeEntry{
				{FQDN: "lh3.googleusercontent.com", Parent: "googleusercontent.com",
					IPs: []string{"142.251.1.132"}},
			},
		},
		{
			Name: "AWG_5_other_1",
			Entries: []runtimeEntry{
				{FQDN: "yt3.googleusercontent.com", Parent: "googleusercontent.com",
					IPs: []string{"142.251.1.132"}},
			},
		},
	}
	lister := &fakeLister{lists: []dnsroute.DomainList{
		{ID: "list_5", Name: "Хостинги"},
		{ID: "list_6", Name: "YouTube"},
	}}

	m := buildIPRuleMap(context.Background(), groups, lister)

	hits := m["142.251.1.132"]
	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(hits))
	}
}

func TestBuildIPRuleMap_UnknownListID(t *testing.T) {
	// Group exists on router but list was deleted from awg-manager — fall back gracefully.
	groups := []runtimeGroup{
		{
			Name: "AWG_99_orphan_1",
			Entries: []runtimeEntry{
				{FQDN: "x.example.com", Parent: "example.com", IPs: []string{"1.2.3.4"}},
			},
		},
	}
	lister := &fakeLister{lists: []dnsroute.DomainList{
		{ID: "list_6", Name: "YouTube"},
	}}

	m := buildIPRuleMap(context.Background(), groups, lister)

	hits := m["1.2.3.4"]
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1 (orphan group should still produce a hit with unknown name)", len(hits))
	}
	if hits[0].ListID != "list_99" {
		t.Errorf("ListID = %q, want list_99", hits[0].ListID)
	}
	if hits[0].ListName != "" {
		t.Errorf("ListName = %q, want \"\" for unknown list", hits[0].ListName)
	}
}

func TestBuildIPRuleMap_ListerError(t *testing.T) {
	// If the lister fails, we still return rule hits but with empty ListName.
	groups := []runtimeGroup{
		{
			Name: "AWG_6_youtube_1",
			Entries: []runtimeEntry{
				{FQDN: "m.youtube.com", Parent: "youtube.com", IPs: []string{"142.251.1.100"}},
			},
		},
	}
	lister := &fakeLister{err: context.Canceled}

	m := buildIPRuleMap(context.Background(), groups, lister)
	hits := m["142.251.1.100"]
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(hits))
	}
	if hits[0].ListID != "list_6" {
		t.Errorf("ListID = %q, want list_6", hits[0].ListID)
	}
}

func TestBuildIPRuleMap_NonAWGGroupSkipped(t *testing.T) {
	// Object groups not created by awg-manager (no AWG_ prefix) are ignored.
	groups := []runtimeGroup{
		{
			Name: "Some_Other_Group",
			Entries: []runtimeEntry{
				{FQDN: "x.com", Parent: "x.com", IPs: []string{"1.1.1.1"}},
			},
		},
	}
	lister := &fakeLister{lists: nil}

	m := buildIPRuleMap(context.Background(), groups, lister)
	if len(m) != 0 {
		t.Errorf("non-AWG group should be ignored, got %d entries", len(m))
	}
}
