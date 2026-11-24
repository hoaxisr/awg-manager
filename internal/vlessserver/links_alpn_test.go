package vlessserver

import (
	"strings"
	"testing"
)

// F868: TLS через роутер снимает nginx прошивки (только HTTP/1.1, HTTP/2 мы
// не включаем) — ссылка не должна звать h2, иначе Xray сразу шлёт HTTP/2 и
// не подключается. Внешний CDN h2 держит.
func TestShareLinksALPNByTermination(t *testing.T) {
	client := Client{ID: "b831381d-6324-4d53-ad4f-8cda48b30811", Remark: "t", Enabled: true}
	for _, tc := range []struct {
		name, domain, way string
		h2                bool
	}{
		{"keendns", "awgmx.example.keenetic.pro", "", false},
		{"direct", "vpn.example.com", "direct", false},
		{"cdn", "cdn.example.com", "external_https", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Transport: "xhttp", PublicDomain: tc.domain, ConnectionWay: tc.way}
			links, err := BuildShareLinks(cfg, client)
			if err != nil {
				t.Fatal(err)
			}
			for name, s := range map[string]string{"vless": links.VlessURL, "happ": links.HappJSON, "singbox": links.SingboxJSON, "mihomo": links.MihomoYAML} {
				if got := strings.Contains(s, "h2"); got != tc.h2 {
					t.Errorf("%s link: h2 present=%v, want %v\n%s", name, got, tc.h2, s)
				}
				if !strings.Contains(s, "http/1.1") {
					t.Errorf("%s link lacks http/1.1", name)
				}
			}
		})
	}
}
