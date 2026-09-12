package mcp

import (
	"net"
	"testing"
)

// TestDomainCovers — списки маршрутизации сопоставляются по суффиксу, и
// именно здесь легко ошибиться: «notyoutube.com» заканчивается на
// «youtube.com», но родительским доменом ему не является. Наивная
// проверка суффикса отправила бы чужой домен в чужой туннель.
func TestDomainCovers(t *testing.T) {
	cases := []struct {
		entry, target string
		want          bool
	}{
		{"youtube.com", "youtube.com", true},
		{"youtube.com", "www.youtube.com", true},
		{"youtube.com", "a.b.youtube.com", true},
		{"youtube.com", "notyoutube.com", false},
		{"youtube.com", "youtube.com.evil.net", false},
		{"youtube.com", "com", false},
		{"YouTube.COM", "www.youtube.com", true},
		{"youtube.com.", "www.youtube.com", true},
		{"", "www.youtube.com", false},
		{"  ", "www.youtube.com", false},
	}
	for _, c := range cases {
		if got := domainCovers(c.entry, normalizeDomain(c.target)); got != c.want {
			t.Errorf("domainCovers(%q, %q) = %v, want %v", c.entry, c.target, got, c.want)
		}
	}
}

func TestUnevaluatableEntry(t *testing.T) {
	// Only the two tag prefixes the daemon itself recognises count
	// (dnsroute/impl.go): a bare IPv6 literal also contains colons and
	// must not be mistaken for a tag.
	cases := map[string]bool{
		"geosite:google":  true,
		"geoip:ru":        true,
		"youtube.com":     false,
		"10.0.0.0/8":      false,
		"2001:db8::/32":   false,
		"2001:db8::1":     false,
		"example.com:443": false,
	}
	for entry, want := range cases {
		if got := unevaluatableEntry(entry); got != want {
			t.Errorf("unevaluatableEntry(%q) = %v, want %v", entry, got, want)
		}
	}
}

// TestMatchDNSList — список может смешивать домены, подсети и теги.
// Найденное совпадение важнее флага «не всё удалось разобрать», но при
// отсутствии совпадения о неразобранном обязаны сообщить.
func TestMatchDNSList(t *testing.T) {
	ips := []net.IP{net.ParseIP("10.20.5.7").To4()}

	entry, unevaluated := matchDNSList([]string{"geosite:google", "youtube.com"}, "www.youtube.com", nil)
	if entry != "youtube.com" {
		t.Errorf("entry = %q, want the domain that matched", entry)
	}
	if !unevaluated {
		t.Error("the tag was still skipped; the caller may want to know")
	}

	entry, _ = matchDNSList([]string{"10.20.0.0/16"}, "", ips)
	if entry != "10.20.0.0/16" {
		t.Errorf("a CIDR entry must match a resolved address, got %q", entry)
	}

	entry, unevaluated = matchDNSList([]string{"geosite:google"}, "www.youtube.com", ips)
	if entry != "" || !unevaluated {
		t.Errorf("entry=%q unevaluated=%v, want no match and a flag", entry, unevaluated)
	}

	entry, unevaluated = matchDNSList([]string{"other.example", ""}, "www.youtube.com", ips)
	if entry != "" || unevaluated {
		t.Errorf("entry=%q unevaluated=%v, want a clean miss", entry, unevaluated)
	}
}

func TestMatchSubnets(t *testing.T) {
	ips := []net.IP{net.ParseIP("192.0.2.9").To4(), net.ParseIP("10.20.5.7").To4()}

	cidr, hit, _ := matchSubnets([]string{"172.16.0.0/12", " 10.20.0.0/16 "}, ips)
	if cidr != "10.20.0.0/16" || hit != "10.20.5.7" {
		t.Errorf("cidr=%q hit=%q, want the containing subnet and the address inside it", cidr, hit)
	}

	if cidr, _, _ := matchSubnets([]string{"not-a-cidr"}, ips); cidr != "" {
		t.Errorf("a malformed entry must be skipped, got %q", cidr)
	}
	if cidr, _, _ := matchSubnets([]string{"10.20.0.0/16"}, nil); cidr != "" {
		t.Errorf("with nothing resolved there is nothing to match, got %q", cidr)
	}
	// geoip: tags live in Subnets; a list of only those is not a miss.
	if cidr, _, unevaluated := matchSubnets([]string{"geoip:ru"}, ips); cidr != "" || !unevaluated {
		t.Errorf("cidr=%q unevaluated=%v, want no match and the flag", cidr, unevaluated)
	}
}

// TestExplainNoteDoesNotInventAWinner — приоритет правил решает роутер.
// Заявить победителя значило бы уверенно назвать туннель, через который
// трафик может и не пойти.
func TestExplainNoteDoesNotInventAWinner(t *testing.T) {
	out := explainOut{
		DNSMatches:    []explainDNSMatch{{RouteID: "dl-1", TunnelID: "tn-1"}, {RouteID: "dl-2", TunnelID: "tn-2"}},
		StaticMatches: []explainStaticMatch{},
	}
	note := explainNote(&out)
	if note == "" {
		t.Fatal("two matching lists need an explanation")
	}
	if !contains(note, "candidates") {
		t.Errorf("note = %q, want it to stop short of naming a winner", note)
	}

	empty := explainOut{DNSMatches: []explainDNSMatch{}, StaticMatches: []explainStaticMatch{}}
	if note := explainNote(&empty); !contains(note, "default route") {
		t.Errorf("note = %q, want the default route mentioned when nothing matched", note)
	}

	tagged := explainOut{
		DNSMatches: []explainDNSMatch{}, StaticMatches: []explainStaticMatch{},
		UnevaluatedLists: []explainUnevaluated{{RouteID: "dl-geo"}},
	}
	if note := explainNote(&tagged); !contains(note, "unevaluatedLists") {
		t.Errorf("note = %q, want it to warn against concluding the target is unrouted", note)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
