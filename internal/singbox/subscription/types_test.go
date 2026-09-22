package subscription

import "testing"

func TestSubscription_IsFile(t *testing.T) {
	cases := []struct {
		name         string
		s            Subscription
		file, inline bool
	}{
		{"url", Subscription{URL: "https://x"}, false, false},
		{"inline", Subscription{Inline: "vless://x"}, false, true},
		{"file", Subscription{Path: "/opt/etc/s.txt"}, true, false},
		{"file+url", Subscription{Path: "/opt/etc/s.txt", URL: "https://x"}, false, false},
		{"file+inline", Subscription{Path: "/opt/etc/s.txt", Inline: "vless://x"}, false, true},
	}
	for _, c := range cases {
		if got := c.s.IsFile(); got != c.file {
			t.Errorf("%s: IsFile=%v want %v", c.name, got, c.file)
		}
		if got := c.s.IsInline(); got != c.inline {
			t.Errorf("%s: IsInline=%v want %v", c.name, got, c.inline)
		}
	}
}
