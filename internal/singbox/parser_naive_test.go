package singbox

import (
	"encoding/json"
	"testing"
)

func TestParseNaive_Full(t *testing.T) {
	got, err := parseNaive("naive+https://user123:pass456@jp.example.com:443#Japan")
	if err != nil {
		t.Fatal(err)
	}
	if got.Tag != "Japan" || got.Server != "jp.example.com" || got.Port != 443 {
		t.Errorf("basic: %+v", got)
	}
	var raw map[string]any
	_ = json.Unmarshal(got.Outbound, &raw)
	if raw["type"] != "naive" {
		t.Error("type")
	}
	if raw["username"] != "user123" || raw["password"] != "pass456" {
		t.Error("creds")
	}
	if raw["network"] != "tcp" {
		t.Error("network")
	}
}

func TestParseNaive_Missing(t *testing.T) {
	cases := []string{
		"vless://u:p@host:443",         // wrong scheme
		"naive+https://host:443",       // no user:pass
		"naive+https://user@host:443",  // no pass
		"naive+https://u:p@:443",       // no host
		"naive+https://u:p@host",       // no port
		"naive+https://u:p@host:abc",   // non-numeric port
		"naive+https://u:p@host:0",     // out of range
		"naive+https://u:p@host:99999", // out of range
	}
	for _, c := range cases {
		if _, err := parseNaive(c); err == nil {
			t.Errorf("expected error for %q", c)
		}
	}
}
