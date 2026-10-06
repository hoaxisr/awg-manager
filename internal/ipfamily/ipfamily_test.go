package ipfamily

import (
	"slices"
	"testing"
)

// TestIsIPv6 — #1011: IPv6 приходит в список и подсетью, и голым адресом (он
// попадает в домены). Оба должны отсеиваться, а IPv4, теги и домены — нет.
// IPv4-mapped формы записаны как IPv6 и на роутер уходят IPv6-записью.
func TestIsIPv6(t *testing.T) {
	for in, want := range map[string]bool{
		"2001:db8::/32":       true,
		"2001:db8::1":         true,
		" 2a00:1450::1 ":      true,
		"fe80::/10":           true,
		"::1":                 true,
		"::ffff:1.2.3.4":      true,
		"::ffff:10.0.0.0/104": true,
		"10.0.0.0/8":          false,
		"10.1.2.3/8":          false,
		"1.2.3.4":             false,
		"example.com":         false,
		".googlevideo.com":    false,
		"geoip:RU":            false,
		"geosite:GOOGLE":      false,
		"":                    false,
	} {
		if got := IsIPv6(in); got != want {
			t.Errorf("IsIPv6(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestWithoutIPv6KeepsOrder(t *testing.T) {
	got := WithoutIPv6([]string{"b.com", "2001:db8::/32", "10.0.0.0/8", "::ffff:10.0.0.0/104", "a.com"})
	if want := []string{"b.com", "10.0.0.0/8", "a.com"}; !slices.Equal(got, want) {
		t.Fatalf("WithoutIPv6 = %v, want %v", got, want)
	}
}
