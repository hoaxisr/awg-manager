package hydraroute

import (
	"reflect"
	"testing"
)

// Эталоны сняты с сериализатора HRweb 1.65.12-1 (buildDomainFile /
// buildCIDRFile после parse* + linkGroups), см. спеку F629.
func TestRenderRuleFiles_MatchesHRweb(t *testing.T) {
	cases := []struct {
		name               string
		inDomain, inIP     string
		wantDomain, wantIP string
	}{
		{"belka", "##\ntelegram.org,t.me/Belka", "##\n/Belka\n91.108.4.0/22",
			"##\ntelegram.org,t.me/Belka", "##\n/Belka\n91.108.4.0/22"},
		{"domains no header", "a.com,b.com/Belka", "", "a.com,b.com/Belka", ""},
		{"cidr no header", "", "/Belka\n1.1.1.0/24", "", "/Belka\n1.1.1.0/24"},
		{"pair, flags differ", "##Tg\n#t.me/Belka", "##Tg\n/Belka\n91.108.4.0/22",
			"##Tg\n#t.me/Belka", "##Tg\n/Belka\n91.108.4.0/22"},
		{"duplicate descriptions", "##X\na.com/P\n##X\nb.com/P", "##X\n/P\n1.0.0.0/8",
			"##X\na.com/P\n##X\nb.com/P", "##X\n/P\n1.0.0.0/8"},
		{"header vs no header", "##X\na.com/P", "/P\n1.0.0.0/8", "##X\na.com/P", "/P\n1.0.0.0/8"},
		{"old awg-manager format",
			"## Youtube\nyoutube.com/nwg0\n## Mixed\nexample.com/nwg0\n", "## Mixed\n/nwg0\n10.0.0.0/8\n\n",
			"##Youtube\nyoutube.com/nwg0\n##Mixed\nexample.com/nwg0", "##Mixed\n/nwg0\n10.0.0.0/8"},
		{"targets sorted", "x.com/Zeta\ny.com/Alpha", "", "y.com/Alpha\nx.com/Zeta", ""},
		{"disabled cidr", "", "##Off\n#/P\n2.0.0.0/8", "", "##Off\n#/P\n2.0.0.0/8"},
		// Склеиваются только половины с явным заголовком: пустой `##` с одной
		// стороны и отсутствие заголовка с другой — разные группы.
		{"empty header vs no header", "##\na.com/P", "/P\n1.0.0.0/8", "a.com/P", "/P\n1.0.0.0/8"},
		{"no header vs empty header", "a.com/P", "##\n/P\n1.0.0.0/8", "a.com/P", "/P\n1.0.0.0/8"},
		{"blank line between header and block", "", "##X\n\n/P\n1.0.0.0/8", "", "##X\n/P\n1.0.0.0/8"},
		{"non-ASCII targets", "x.com/Яблоко\ny.com/apple\nz.com/Zed", "", "z.com/Zed\ny.com/apple\nx.com/Яблоко", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotD, gotI := renderRuleFiles(parseRuleFiles(tc.inDomain, tc.inIP))
			if gotD != tc.wantDomain {
				t.Errorf("domain.conf:\n got %q\nwant %q", gotD, tc.wantDomain)
			}
			if gotI != tc.wantIP {
				t.Errorf("ip.list:\n got %q\nwant %q", gotI, tc.wantIP)
			}
		})
	}
}

// Сознательные расхождения с HRweb — ожидания НАШИ, не JS.
func TestRenderRuleFiles_DeliberateDivergences(t *testing.T) {
	cases := []struct {
		name               string
		inDomain, inIP     string
		wantDomain, wantIP string
	}{
		// Релизные awg-manager до 5434e2147 писали выключенное IP-правило с
		// `#` на каждой подсети. HRweb держит `#10.0.0.0/8` как есть и не
		// может сохранить файл (#1022), демон считает её записью. Снимаем `#`
		// и выключаем половину — как старый parseIPList.
		{"legacy disabled ip.list", "", "## X\n#/nwg0\n#10.0.0.0/8\n\n", "", "##X\n#/nwg0\n10.0.0.0/8"},
		{"legacy disabled ipv6", "", "/P\n#2001:db8::/32", "", "#/P\n2001:db8::/32"},
		// `#…`, который не разбирается как подсеть/адрес/geoip, — комментарий
		// HRweb: держим как есть, половина остаётся включённой.
		{"comment #1 kept", "", "/P\n#1\n1.0.0.0/8", "", "/P\n#1\n1.0.0.0/8"},
		{"comment #2024 kept", "", "/P\n#2024\n1.0.0.0/8", "", "/P\n#2024\n1.0.0.0/8"},
		{"comment #face kept", "", "/P\n#face\n1.0.0.0/8", "", "/P\n#face\n1.0.0.0/8"},
		// Порядок CIDR-блоков цели — порядок групп (парный блок рядом со своей
		// доменной группой). HRweb сохранил бы порядок ip.list. Одна цель =
		// один ipset, на маршрутизацию не влияет.
		{"cidr order follows groups", "##S\ns.com/P", "##N\n/P\n1.0.0.0/8\n##S\n/P\n2.0.0.0/8",
			"##S\ns.com/P", "##S\n/P\n2.0.0.0/8\n##N\n/P\n1.0.0.0/8"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotD, gotI := renderRuleFiles(parseRuleFiles(tc.inDomain, tc.inIP))
			if gotD != tc.wantDomain || gotI != tc.wantIP {
				t.Errorf("got %q / %q\nwant %q / %q", gotD, gotI, tc.wantDomain, tc.wantIP)
			}
		})
	}
}

func TestParseRuleFiles_ServiceSectionEdgeCases(t *testing.T) {
	// Служебное имя с чужой целью — обычная выключенная группа цели nwg0.
	rs := parseRuleFiles("", "##impossible to use\n#/nwg0\ngeoip:ru\n10.0.0.0/8")
	g := rs.Target("nwg0")
	if g == nil || len(g.Groups) != 1 || !g.Groups[0].SubnetsDisabled || len(rs.Oversized) != 0 {
		t.Fatalf("got %+v oversized=%v", rs.Targets, rs.Oversized)
	}
	// Служебная секция без geoip-строк не пишется.
	_, ip := renderRuleFiles(parseRuleFiles("", "##impossible to use\n#/Too-big-geoip-tag\n"))
	if ip != "" {
		t.Fatalf("empty service section written: %q", ip)
	}
}

func TestParseRuleFiles_Model(t *testing.T) {
	rs := parseRuleFiles("##X\na.com/P\n##X\nb.com/P\nz.com/Q", "##X\n/P\n1.0.0.0/8\n/P\n9.0.0.0/8")
	want := []Target{
		{Name: "P", Groups: []Group{
			{Description: "X", Domains: []string{"a.com"}, Subnets: []string{"1.0.0.0/8"}},
			{Description: "X", Domains: []string{"b.com"}},
			{Subnets: []string{"9.0.0.0/8"}},
		}},
		{Name: "Q", Groups: []Group{{Domains: []string{"z.com"}}}},
	}
	if !reflect.DeepEqual(rs.Targets, want) {
		t.Fatalf("targets:\n got %+v\nwant %+v", rs.Targets, want)
	}
}

func TestParseRuleFiles_BlankLineEndsBlock(t *testing.T) {
	rs := parseRuleFiles("", "/P\n1.0.0.0/8\n\n2.0.0.0/8")
	if got := rs.Target("P").Groups[0].Subnets; !reflect.DeepEqual(got, []string{"1.0.0.0/8"}) {
		t.Fatalf("subnets = %v, want only the line before the blank one (daemon semantics)", got)
	}
}

func TestParseRuleFiles_CRLF(t *testing.T) {
	rs := parseRuleFiles("##A\r\na.com/P\r\n", "##A\r\n/P\r\n1.0.0.0/8\r\n")
	g := rs.Target("P").Groups[0]
	if g.Description != "A" || g.Domains[0] != "a.com" || g.Subnets[0] != "1.0.0.0/8" {
		t.Fatalf("CRLF not stripped: %+v", g)
	}
}

func TestParseRuleFiles_OversizedSection(t *testing.T) {
	ip := "##A\n/P\n1.0.0.0/8\n\n##impossible to use\n#/Too-big-geoip-tag\ngeoip:cn\ngeoip:us"
	rs := parseRuleFiles("", ip)
	if !reflect.DeepEqual(rs.Oversized, []string{"geoip:cn", "geoip:us"}) {
		t.Fatalf("oversized = %v", rs.Oversized)
	}
	if rs.Target("Too-big-geoip-tag") != nil {
		t.Fatal("service section leaked into targets")
	}
	_, gotIP := renderRuleFiles(rs)
	want := "##A\n/P\n1.0.0.0/8\n##impossible to use\n#/Too-big-geoip-tag\ngeoip:cn\ngeoip:us"
	if gotIP != want {
		t.Fatalf("ip.list:\n got %q\nwant %q", gotIP, want)
	}
}

func TestGroupEnabled(t *testing.T) {
	cases := []struct {
		g    Group
		want bool
	}{
		{Group{Domains: []string{"a"}, DomainsDisabled: true}, false},
		{Group{Subnets: []string{"1.0.0.0/8"}}, true},
		{Group{Domains: []string{"a"}, Subnets: []string{"1.0.0.0/8"}, DomainsDisabled: true}, true},
		{Group{Domains: []string{"a"}, Subnets: []string{"1.0.0.0/8"}, DomainsDisabled: true, SubnetsDisabled: true}, false},
		// флаг отсутствующей половины не в счёт
		{Group{Domains: []string{"a"}, SubnetsDisabled: true}, true},
	}
	for i, tc := range cases {
		if got := tc.g.Enabled(); got != tc.want {
			t.Errorf("case %d: Enabled() = %v, want %v", i, got, tc.want)
		}
	}
}

func TestRuleRevision(t *testing.T) {
	a := ruleRevision("x", "y")
	if len(a) != 12 {
		t.Fatalf("len = %d", len(a))
	}
	if a == ruleRevision("xy", "") {
		t.Fatal("separator missing: different file pairs give the same revision")
	}
	if a != parseRuleFiles("x", "y").Revision {
		t.Fatal("parseRuleFiles must fill Revision from raw contents")
	}
}

func TestParseRuleFiles_EmptyTargetOpensNoBlock(t *testing.T) {
	rs := parseRuleFiles("", "/\n1.0.0.0/8\n/P\n2.0.0.0/8")
	if len(rs.Targets) != 1 || rs.Targets[0].Name != "P" {
		t.Fatalf("targets = %+v", rs.Targets)
	}
	if _, ip := renderRuleFiles(rs); ip != "/P\n2.0.0.0/8" {
		t.Fatalf("ip.list = %q", ip)
	}
}

func TestParseRuleFiles_CommentLineInBlockKept(t *testing.T) {
	in := "/P\n# office\n1.0.0.0/8"
	rs := parseRuleFiles("", in)
	g := rs.Target("P").Groups[0]
	if !reflect.DeepEqual(g.Subnets, []string{"# office", "1.0.0.0/8"}) || g.SubnetsDisabled {
		t.Fatalf("got %+v", g)
	}
	if _, ip := renderRuleFiles(rs); ip != in {
		t.Fatalf("round-trip = %q", ip)
	}
}
