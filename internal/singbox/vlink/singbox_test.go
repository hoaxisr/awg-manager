package vlink

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestIsSingboxJSON(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "single config object with outbounds",
			body: `{"outbounds":[{"type":"vless","tag":"x","server":"h","server_port":443,"uuid":"u"}]}`,
			want: true,
		},
		{
			name: "single config object with outbounds and dns/route siblings",
			body: `{"dns":{"servers":[]},"route":{"rules":[]},"outbounds":[{"type":"trojan","tag":"x","server":"h","server_port":443,"password":"p"}]}`,
			want: true,
		},
		{
			name: "object with leading whitespace",
			body: "\n\t  " + `{"outbounds":[{"type":"vless","tag":"x","server":"h","server_port":443,"uuid":"u"}]}`,
			want: true,
		},
		{
			name: "array of configs",
			body: `[{"outbounds":[{"type":"vless","tag":"a","server":"h","server_port":443,"uuid":"u"}]},{"outbounds":[]}]`,
			want: true,
		},
		{
			name: "bare outbounds array (Hiddify-style)",
			body: `[{"type":"vless","tag":"a","server":"h","server_port":443,"uuid":"u"}]`,
			want: true,
		},
		{
			name: "object without outbounds key",
			body: `{"dns":{"servers":[]},"route":{"rules":[]}}`,
			want: false,
		},
		{
			name: "outbounds wrong shape (object not array)",
			body: `{"outbounds":{"type":"vless"}}`,
			want: false,
		},
		{
			name: "empty array",
			body: `[]`,
			want: false,
		},
		{
			name: "array element without type+tag",
			body: `[{"foo":1}]`,
			want: false,
		},
		{
			name: "non-JSON share-link plain",
			body: `vless://uuid@host:443?security=tls`,
			want: false,
		},
		{
			name: "html prefix",
			body: `<!DOCTYPE html><html></html>`,
			want: false,
		},
		{
			name: "clash YAML",
			body: "proxies:\n  - name: a\n    type: vless\n",
			want: false,
		},
		{
			name: "empty body",
			body: ``,
			want: false,
		},
		{
			name: "broken json",
			body: `{"outbounds":[`,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsSingboxJSON([]byte(tc.body)); got != tc.want {
				t.Errorf("IsSingboxJSON = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseSingboxBody_SingleConfig_AllSupportedTypes(t *testing.T) {
	body := `{
		"outbounds": [
			{
				"type": "vless",
				"tag": "🇳🇱 NL #1",
				"server": "nl.example.com",
				"server_port": 443,
				"uuid": "00000000-0000-0000-0000-000000000001",
				"flow": "xtls-rprx-vision",
				"tls": {
					"enabled": true,
					"server_name": "sni.example.com",
					"reality": {"enabled": true, "public_key": "PK", "short_id": "SID"},
					"utls": {"enabled": true, "fingerprint": "chrome"}
				}
			},
			{
				"type": "trojan",
				"tag": "🇩🇪 DE",
				"server": "de.example.com",
				"server_port": 8443,
				"password": "secret",
				"transport": {"type": "ws", "path": "/path", "headers": {"Host": "h.example.com"}}
			},
			{
				"type": "shadowsocks",
				"tag": "ss-1",
				"server": "ss.example.com",
				"server_port": 8388,
				"method": "aes-256-gcm",
				"password": "ssp"
			},
			{
				"type": "hysteria2",
				"tag": "hy2-1",
				"server": "hy.example.com",
				"server_port": 8443,
				"password": "hyp",
				"obfs": {"type": "salamander", "password": "obfsp"}
			}
		]
	}`
	res := ParseSingboxBody([]byte(body))
	if len(res.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", res.Errors)
	}
	if got := len(res.Outbounds); got != 4 {
		t.Fatalf("want 4 outbounds, got %d", got)
	}
	wantProto := []string{"vless", "trojan", "shadowsocks", "hysteria2"}
	wantLabel := []string{"🇳🇱 NL #1", "🇩🇪 DE", "ss-1", "hy2-1"}
	for i, p := range res.Outbounds {
		if p.Protocol != wantProto[i] {
			t.Errorf("[%d] Protocol = %q, want %q", i, p.Protocol, wantProto[i])
		}
		if p.Label != wantLabel[i] {
			t.Errorf("[%d] Label = %q, want %q", i, p.Label, wantLabel[i])
		}
		if p.Server == "" || p.Port == 0 {
			t.Errorf("[%d] empty server/port: %+v", i, p)
		}
	}
}

func TestParseSingboxBody_RawCopyPreservesAllFields(t *testing.T) {
	body := `{"outbounds":[{
		"type": "vless",
		"tag": "x",
		"server": "h",
		"server_port": 443,
		"uuid": "u",
		"flow": "xtls-rprx-vision",
		"tls": {"enabled": true, "reality": {"enabled": true, "public_key": "PK"}, "utls": {"enabled": true, "fingerprint": "chrome"}},
		"multiplex": {"enabled": true, "protocol": "smux"}
	}]}`
	res := ParseSingboxBody([]byte(body))
	if len(res.Outbounds) != 1 {
		t.Fatalf("want 1 outbound, got %d (errors=%v)", len(res.Outbounds), res.Errors)
	}
	var ob map[string]any
	if err := json.Unmarshal(res.Outbounds[0].Outbound, &ob); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	// Raw-copy must preserve all keys including ones our share-link factory
	// doesn't understand (utls, multiplex).
	if _, ok := ob["multiplex"].(map[string]any); !ok {
		t.Errorf("multiplex section dropped: %+v", ob)
	}
	tls, _ := ob["tls"].(map[string]any)
	if _, ok := tls["utls"].(map[string]any); !ok {
		t.Errorf("tls.utls section dropped: %+v", tls)
	}
	if _, ok := tls["reality"].(map[string]any); !ok {
		t.Errorf("tls.reality section dropped: %+v", tls)
	}
}

func TestParseSingboxBody_ArrayOfConfigs_FlatOrder(t *testing.T) {
	body := `[
		{"outbounds":[
			{"type":"vless","tag":"a","server":"h1","server_port":443,"uuid":"u1"},
			{"type":"trojan","tag":"b","server":"h2","server_port":443,"password":"p"}
		]},
		{"outbounds":[
			{"type":"hysteria2","tag":"c","server":"h3","server_port":443,"password":"hp"}
		]}
	]`
	res := ParseSingboxBody([]byte(body))
	if len(res.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", res.Errors)
	}
	if got := len(res.Outbounds); got != 3 {
		t.Fatalf("want 3 outbounds, got %d", got)
	}
	wantOrder := []string{"a", "b", "c"}
	for i, p := range res.Outbounds {
		if p.Label != wantOrder[i] {
			t.Errorf("[%d] Label = %q, want %q (flatten order broken)", i, p.Label, wantOrder[i])
		}
	}
}

func TestParseSingboxBody_BareOutboundsArray(t *testing.T) {
	body := `[
		{"type":"vless","tag":"a","server":"h","server_port":443,"uuid":"u"},
		{"type":"trojan","tag":"b","server":"h","server_port":443,"password":"p"}
	]`
	res := ParseSingboxBody([]byte(body))
	if len(res.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", res.Errors)
	}
	if got := len(res.Outbounds); got != 2 {
		t.Fatalf("want 2 outbounds, got %d", got)
	}
}

func TestParseSingboxBody_SkipsServiceTypesAndVmess(t *testing.T) {
	body := `{"outbounds":[
		{"type":"direct","tag":"direct"},
		{"type":"block","tag":"block"},
		{"type":"dns","tag":"dns-out"},
		{"type":"selector","tag":"sel","outbounds":["a","b"]},
		{"type":"urltest","tag":"ut","outbounds":["a","b"]},
		{"type":"vmess","tag":"vm","server":"h","server_port":443,"uuid":"u"},
		{"type":"vless","tag":"keep","server":"h","server_port":443,"uuid":"u"}
	]}`
	res := ParseSingboxBody([]byte(body))
	if len(res.Errors) != 0 {
		t.Errorf("service types must not produce ParseError, got %v", res.Errors)
	}
	if res.SkippedVmess != 1 {
		t.Errorf("SkippedVmess = %d, want 1", res.SkippedVmess)
	}
	if res.SkippedUnsupp != 0 {
		t.Errorf("SkippedUnsupp = %d, want 0 (service types aren't 'unsupported proxies')", res.SkippedUnsupp)
	}
	if got := len(res.Outbounds); got != 1 {
		t.Fatalf("want 1 outbound (the vless), got %d", got)
	}
	if res.Outbounds[0].Label != "keep" {
		t.Errorf("kept outbound label = %q, want %q", res.Outbounds[0].Label, "keep")
	}
}

func TestParseSingboxBody_UnsupportedProxyTypes(t *testing.T) {
	body := `{"outbounds":[
		{"type":"tuic","tag":"t","server":"h","server_port":443},
		{"type":"wireguard","tag":"w","server":"h","server_port":443},
		{"type":"naive","tag":"n","server":"h","server_port":443},
		{"type":"vless","tag":"keep","server":"h","server_port":443,"uuid":"u"}
	]}`
	res := ParseSingboxBody([]byte(body))
	if res.SkippedUnsupp != 3 {
		t.Errorf("SkippedUnsupp = %d, want 3", res.SkippedUnsupp)
	}
	if got := len(res.Errors); got != 3 {
		t.Errorf("want 3 ParseErrors, got %d", got)
	}
	for _, e := range res.Errors {
		if !strings.HasPrefix(e.Scheme, "sing-box:") {
			t.Errorf("ParseError.Scheme = %q, want prefix sing-box:", e.Scheme)
		}
	}
	if len(res.Outbounds) != 1 {
		t.Fatalf("kept outbounds = %d, want 1", len(res.Outbounds))
	}
}

func TestParseSingboxBody_ValidationErrors(t *testing.T) {
	cases := []struct {
		name      string
		outbound  string
		wantSubst string
	}{
		{
			name:      "vless missing uuid",
			outbound:  `{"type":"vless","tag":"x","server":"h","server_port":443}`,
			wantSubst: "uuid",
		},
		{
			name:      "trojan missing password",
			outbound:  `{"type":"trojan","tag":"x","server":"h","server_port":443}`,
			wantSubst: "password",
		},
		{
			name:      "shadowsocks missing method",
			outbound:  `{"type":"shadowsocks","tag":"x","server":"h","server_port":443,"password":"p"}`,
			wantSubst: "method",
		},
		{
			name:      "hysteria2 missing password",
			outbound:  `{"type":"hysteria2","tag":"x","server":"h","server_port":443}`,
			wantSubst: "password",
		},
		{
			name:      "missing server",
			outbound:  `{"type":"vless","tag":"x","server_port":443,"uuid":"u"}`,
			wantSubst: "server",
		},
		{
			name:      "port zero",
			outbound:  `{"type":"vless","tag":"x","server":"h","server_port":0,"uuid":"u"}`,
			wantSubst: "out of range",
		},
		{
			name:      "port out of range",
			outbound:  `{"type":"vless","tag":"x","server":"h","server_port":99999,"uuid":"u"}`,
			wantSubst: "out of range",
		},
		{
			name:      "no type",
			outbound:  `{"tag":"x","server":"h","server_port":443}`,
			wantSubst: "no type",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{"outbounds":[` + tc.outbound + `]}`
			res := ParseSingboxBody([]byte(body))
			if len(res.Outbounds) != 0 {
				t.Errorf("expected 0 outbounds, got %d", len(res.Outbounds))
			}
			if len(res.Errors) != 1 {
				t.Fatalf("expected 1 error, got %v", res.Errors)
			}
			if !strings.Contains(res.Errors[0].Message, tc.wantSubst) {
				t.Errorf("error %q missing substring %q", res.Errors[0].Message, tc.wantSubst)
			}
		})
	}
}

func TestParseSingboxBody_BrokenJSON(t *testing.T) {
	res := ParseSingboxBody([]byte(`{"outbounds":[`))
	if len(res.Errors) != 1 {
		t.Fatalf("want 1 error, got %v", res.Errors)
	}
	if !strings.Contains(res.Errors[0].Message, "json parse") {
		t.Errorf("expected json parse error, got %q", res.Errors[0].Message)
	}
}

func TestParseSingboxBody_TagBecomesLabel(t *testing.T) {
	body := `{"outbounds":[{"type":"vless","tag":"🚀 emoji name","server":"h","server_port":443,"uuid":"u"}]}`
	res := ParseSingboxBody([]byte(body))
	if len(res.Outbounds) != 1 {
		t.Fatalf("want 1 outbound, got %d (err=%v)", len(res.Outbounds), res.Errors)
	}
	if res.Outbounds[0].Label != "🚀 emoji name" {
		t.Errorf("Label = %q, want emoji-preserved tag", res.Outbounds[0].Label)
	}
	// ParsedOutbound.Tag should remain empty — the diff logic assigns the
	// stable tag downstream.
	if res.Outbounds[0].Tag != "" {
		t.Errorf("Tag = %q, want empty (assigned downstream)", res.Outbounds[0].Tag)
	}
}
