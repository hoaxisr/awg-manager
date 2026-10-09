package aiassistant

import (
	"strings"
	"testing"
)

func TestLookupKnownError(t *testing.T) {
	tests := []struct {
		input       string
		wantID      string
		wantCat     string
		wantSnippet string
	}{
		{
			input:       "start awg10 [wg]: awg setconf opkgtun10: exit status 1 (exit 1, stderr: Line unrecognized: ` H1=' Configuration parsing error)",
			wantID:      "awg_syntax_leading_space",
			wantCat:     "AmneziaWG",
			wantSnippet: "пробел перед параметром",
		},
		{
			input:       `start awg11 [ndms]: set address: set address OpkgTun11: router reported error: "OpkgTun11": system failed [0xcffd0218].`,
			wantID:      "ndms_0xcffd0218_subnet_conflict",
			wantCat:     "KeeneticOS (NDMS)",
			wantSnippet: "Конфликт IP-адресов",
		},
		{
			input:       "clear address OpkgTun0: system failed [0xcffd0217].",
			wantID:      "ndms_0xcffd0217_clear_address",
			wantCat:     "KeeneticOS (NDMS)",
			wantSnippet: "очистки",
		},
		{
			input:       "OpkgTun0: system failed [0xcffd01b9]",
			wantID:      "ndms_0xcffd01b9_interface_down",
			wantCat:     "KeeneticOS (NDMS)",
			wantSnippet: "выключен",
		},
		{
			input:       "router reported error: allow-ips already in use",
			wantID:      "ndms_allow_ips_already_in_use",
			wantCat:     "KeeneticOS (NDMS)",
			wantSnippet: "AllowedIPs",
		},
		{
			input:       "FATAL[0000] initialize outbound[2]: uTLS is required by reality client: exit status 1",
			wantID:      "singbox_reality_utls_missing",
			wantCat:     "Sing-box",
			wantSnippet: "Reality",
		},
		{
			input:       "mihomo: mixed-port bind error: listen tcp :7890: bind: address already in use",
			wantID:      "mihomo_mixed_port_in_use",
			wantCat:     "Mihomo",
			wantSnippet: "mixed-port",
		},
		{
			input:       "listen tcp 0.0.0.0:80: bind: address already in use",
			wantID:      "errno_eaddrinuse_98",
			wantCat:     "Ядро Linux",
			wantSnippet: "EADDRINUSE",
		},
		{
			input:       "resolving host failed: name or service not known",
			wantID:      "awg_endpoint_dns_failed",
			wantCat:     "AmneziaWG",
			wantSnippet: "DNS",
		},
		{
			input:       "Out of memory: Kill process 1234 (mihomo) score 250 or sacrifice child",
			wantID:      "sys_oom_killer",
			wantCat:     "Система и Entware",
			wantSnippet: "Out of Memory",
		},
		{
			input:       "mount /dev/sda1: read-only file system",
			wantID:      "errno_erofs_30",
			wantCat:     "Ядро Linux",
			wantSnippet: "EROFS",
		},
		{
			input:       "x509: certificate is not yet valid: clock skew detected",
			wantID:      "sys_ntp_clock_skew_1970",
			wantCat:     "Система и Entware",
			wantSnippet: "NTP",
		},
		{
			input:       "download subscription failed: HTTP 403 Forbidden: Cloudflare bot challenge",
			wantID:      "sub_http_403_forbidden",
			wantCat:     "Подписки и прокси",
			wantSnippet: "403 Forbidden",
		},
		{
			input:       "dns server bind error: listen udp :53: bind: address already in use",
			wantID:      "dns_port_53_in_use",
			wantCat:     "DNS и Маршрутизация",
			wantSnippet: "Порт 53",
		},
	}

	for _, tc := range tests {
		t.Run(tc.wantID, func(t *testing.T) {
			err := LookupKnownError(tc.input)
			if err == nil {
				t.Fatalf("expected to find error for %q, got nil", tc.input)
			}
			if err.ID != tc.wantID {
				t.Errorf("got ID %q, want %q", err.ID, tc.wantID)
			}
			if err.Category != tc.wantCat {
				t.Errorf("got Category %q, want %q", err.Category, tc.wantCat)
			}
			card := FormatErrorCard(err, tc.input)
			if !strings.Contains(card, tc.wantSnippet) {
				t.Errorf("expected card to contain %q, got: %s", tc.wantSnippet, card)
			}
			if !strings.Contains(card, "Куда тыкнуть мышкой") {
				t.Errorf("expected card to contain mouse click steps, got: %s", card)
			}
		})
	}
}

func TestSearchErrors(t *testing.T) {
	// Search by code
	res := SearchErrors("0xcffd", "")
	if len(res) == 0 {
		t.Errorf("expected to find 0xcffd errors, got 0")
	}

	// Search by category
	resSing := SearchErrors("", "sing-box")
	if len(resSing) == 0 {
		t.Errorf("expected to find sing-box errors, got 0")
	}
	for _, r := range resSing {
		if !strings.Contains(strings.ToLower(r.Category), "sing-box") {
			t.Errorf("expected category sing-box, got %s", r.Category)
		}
	}

	// Search by keyword
	resMem := SearchErrors("память", "")
	if len(resMem) == 0 {
		t.Errorf("expected to find memory errors, got 0")
	}
}

func TestFallbackHeuristicAnalysis(t *testing.T) {
	// Unknown 0xcffd code
	ans, ok := FallbackHeuristicAnalysis("some command failed with 0xcffd9999 error")
	if !ok || !strings.Contains(ans, "0xcffd9999") || !strings.Contains(ans, "Куда тыкнуть мышкой") {
		t.Errorf("failed to handle unknown 0xcffd code: %s", ans)
	}

	// Syntax error
	ans, ok = FallbackHeuristicAnalysis("unexpected token at line 5: parsing error")
	if !ok || !strings.Contains(ans, "Синтаксическая ошибка") {
		t.Errorf("failed to handle syntax error: %s", ans)
	}
}
