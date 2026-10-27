package subscription

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalize_HTMLJSONRoundTrip(t *testing.T) {
	const base = "vless://00000000-1111-2222-3333-444444444444@proxy.example:443?security=tls&sni=cdn.example"
	cases := []struct {
		name             string
		link             string
		escapeApostrophe bool
		escapeSlashes    bool
	}{
		{name: "query parameters", link: base + "&type=ws&path=%2Fsocket#Example"},
		{name: "escaped URL scheme", link: base + "&type=ws&path=%2Fsocket#Example", escapeSlashes: true},
		{name: "HTML characters", link: base + "#Example <A> & 'B' end", escapeApostrophe: true},
		{name: "literal Unicode escape", link: base + "#Literal " + `\u0026` + " end"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Use the encoder's real HTML escaping rather than a hand-written
			// fixture that may already contain decoded query separators.
			payload, err := json.Marshal(struct {
				Line string `json:"line"`
			}{Line: tc.link})
			if err != nil {
				t.Fatal(err)
			}
			raw := string(payload)
			if tc.escapeApostrophe {
				// Some HTML serializers also escape apostrophes in JSON.
				raw = strings.ReplaceAll(raw, "'", `\u0027`)
			}
			if tc.escapeSlashes {
				raw = strings.ReplaceAll(raw, "/", `\/`)
			}
			var decoded struct {
				Line string `json:"line"`
			}
			if err := json.Unmarshal([]byte(raw), &decoded); err != nil || decoded.Line != tc.link {
				t.Fatalf("invalid JSON fixture: %v, decoded link %q", err, decoded.Line)
			}
			body := []byte("<script>" + raw + "</script>")
			links := NormalizeBody(body, "text/html")
			if len(links) != 1 {
				t.Fatalf("got %d links, want 1: %q", len(links), links)
			}
			if links[0] != tc.link {
				t.Fatalf("JSON round trip changed the link: got %q, want %q", links[0], tc.link)
			}
		})
	}
}

func TestParseSubscriptionBody_HTMLJSONConnectionParameters(t *testing.T) {
	const link = "vless://00000000-1111-2222-3333-444444444444@proxy.example:443?security=tls&sni=cdn.example&type=ws&path=%2Fsocket#Example"
	payload, err := json.Marshal(struct {
		Line string `json:"line"`
	}{Line: link})
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("<script>" + string(payload) + "</script>")
	parsed := parseSubscriptionBody(body, "text/html")
	if len(parsed.Outbounds) != 1 {
		t.Fatalf("got %d outbounds, want 1: %+v", len(parsed.Outbounds), parsed)
	}
	var out struct {
		TLS struct {
			Enabled    bool   `json:"enabled"`
			ServerName string `json:"server_name"`
		} `json:"tls"`
		Transport struct {
			Type string `json:"type"`
			Path string `json:"path"`
		} `json:"transport"`
	}
	if err := json.Unmarshal(parsed.Outbounds[0].Outbound, &out); err != nil {
		t.Fatal(err)
	}
	if !out.TLS.Enabled || out.TLS.ServerName != "cdn.example" {
		t.Errorf("TLS parameters lost during HTML import: %+v", out.TLS)
	}
	if out.Transport.Type != "ws" || out.Transport.Path != "/socket" {
		t.Errorf("transport parameters lost during HTML import: %+v", out.Transport)
	}
}
