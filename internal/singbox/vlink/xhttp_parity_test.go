package vlink

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func xhttpTransport(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var ob map[string]any
	if err := json.Unmarshal(raw, &ob); err != nil {
		t.Fatal(err)
	}
	tr, _ := ob["transport"].(map[string]any)
	return tr
}

const xhttpLinkBase = "vless://b831381d-6324-4d53-ad4f-8cda48b30811@h.com:443?type=xhttp&security=tls&sni=h.com&path=/x"

// sessionIDTable/sessionIDLength переносятся парой и только годной: негодную
// форк отвергает при старте всей конфигурации.
func TestXHTTPSessionIDTable(t *testing.T) {
	link := xhttpLinkBase + "&extra=" + url.QueryEscape(`{"sessionIDTable":"Base62","sessionIDLength":"8-12"}`) + "#n"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatal(err)
	}
	tr := xhttpTransport(t, got.Outbound)
	if tr["session_id_table"] != "Base62" || tr["session_id_length"] != "8-12" {
		t.Fatalf("transport = %v", tr)
	}
	back, err := EncodeOutbound(got.Outbound, "n")
	if err != nil || !strings.Contains(back, "sessionIDTable") {
		t.Fatalf("export lost the table: %s %v", back, err)
	}
	for _, bad := range []string{
		`{"sessionIDTable":"hex","sessionIDLength":"4"}`,
		`{"sessionIDTable":"Base62"}`,
		`{"sessionIDLength":"8-12"}`,
	} {
		got, err := ParseLink(xhttpLinkBase + "&extra=" + url.QueryEscape(bad) + "#n")
		if err != nil {
			t.Fatal(err)
		}
		tr := xhttpTransport(t, got.Outbound)
		if tr["session_id_table"] != nil || tr["session_id_length"] != nil {
			t.Errorf("%s carried: %v", bad, tr)
		}
	}
}

func xrayXHTTPWithDownload(mode, download string) []byte {
	return []byte(`[{"remarks":"D","outbounds":[{"protocol":"vless",
		"settings":{"vnext":[{"address":"198.51.100.3","port":443,
			"users":[{"id":"b831381d-6324-4d53-ad4f-8cda48b30811"}]}]},
		"streamSettings":{"network":"xhttp","security":"tls","tlsSettings":{"serverName":"up.example"},
			"xhttpSettings":{"path":"/u","mode":"` + mode + `","downloadSettings":` + download + `}}}]}]`)
}

const xrayDownload = `{"address":"dl.example","port":8443,"network":"xhttp","security":"tls",
	"tlsSettings":{"serverName":"dl.example"},"xhttpSettings":{"path":"/d","xPaddingBytes":"200-400"}}`

// downloadSettings Xray становится download форка: свой адрес, TLS и
// настройки xhttp.
func TestXHTTPDownloadSettings(t *testing.T) {
	res := ParseXrayBody(xrayXHTTPWithDownload("stream-up", xrayDownload))
	if len(res.Outbounds) != 1 {
		t.Fatalf("errors: %v", res.Errors)
	}
	tr := xhttpTransport(t, res.Outbounds[0].Outbound)
	d, _ := tr["download"].(map[string]any)
	tls, _ := d["tls"].(map[string]any)
	if d["server"] != "dl.example" || d["server_port"] != float64(8443) || d["path"] != "/d" ||
		d["x_padding_bytes"] != "200-400" || tls["server_name"] != "dl.example" || d["type"] != nil || d["mode"] != nil {
		t.Fatalf("download = %v", d)
	}
	if _, err := EncodeOutbound(res.Outbounds[0].Outbound, "n"); err == nil {
		t.Fatal("export with download must fail instead of dropping it")
	}
}

// Молча выбросить загрузку нельзя: узел без неё не заработает.
func TestXHTTPDownloadSettingsRejected(t *testing.T) {
	for name, body := range map[string][]byte{
		"stream-one": xrayXHTTPWithDownload("stream-one", xrayDownload),
		"not xhttp":  xrayXHTTPWithDownload("stream-up", `{"address":"dl.example","port":443,"network":"ws"}`),
		"no address": xrayXHTTPWithDownload("stream-up", `{"port":443,"network":"xhttp"}`),
		"finalmask": xrayXHTTPWithDownload("stream-up",
			`{"address":"dl.example","port":443,"network":"xhttp","finalmask":{"tcp":[{"type":"fragment","settings":{"length":10}}]}}`),
	} {
		res := ParseXrayBody(body)
		if len(res.Outbounds) != 0 || len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, "downloadSettings") {
			t.Errorf("%s: outbounds=%d errors=%v", name, len(res.Outbounds), res.Errors)
		}
	}
}

func TestCheckSingboxFatal_XHTTPForkChecks(t *testing.T) {
	ob := map[string]any{"transport": map[string]any{"type": "xhttp", "mode": "stream-one",
		"download": map[string]any{"server": "d", "server_port": 443}}}
	if err := checkSingboxFatal(ob, "vless"); err == nil {
		t.Fatal("download in stream-one must be rejected")
	}
	ob = map[string]any{"transport": map[string]any{"type": "xhttp", "session_id_table": "hex", "session_id_length": "4"}}
	if err := checkSingboxFatal(ob, "vless"); err == nil {
		t.Fatal("too small session id space must be rejected")
	}
}

func clashXHTTPVless(mode string, ds map[string]any) map[string]any {
	return map[string]any{
		"name": "c", "type": "vless", "server": "up.example", "port": 443,
		"uuid": "b831381d-6324-4d53-ad4f-8cda48b30811", "network": "xhttp",
		"tls": true, "servername": "up.example", "client-fingerprint": "chrome",
		"xhttp-opts": map[string]any{
			"path": "/u", "host": "h.example", "mode": mode,
			"x-padding-bytes": "200-400", "session-table": "Base62", "session-length": "8-12",
			"reuse-settings":    map[string]any{"max-concurrency": "2-4"},
			"download-settings": ds,
		},
	}
}

// download-settings mihomo — частичный прокси: незаданное, включая все поля
// xhttp, наследуется у основного узла, заданное его перекрывает.
func TestClashXHTTPDownloadSettings(t *testing.T) {
	cases := map[string]struct {
		ds   map[string]any
		want func(d, tls map[string]any) bool
	}{
		"inherit all but path": {map[string]any{"path": "/d"}, func(d, tls map[string]any) bool {
			x, _ := d["xmux"].(map[string]any)
			return d["server"] == "up.example" && d["server_port"] == float64(443) && d["path"] == "/d" &&
				d["host"] == "h.example" && d["x_padding_bytes"] == "200-400" && d["session_id_table"] == "Base62" &&
				x["max_concurrency"] == "2-4" && tls["server_name"] == "up.example" && d["download"] == nil
		}},
		"null inherits": {map[string]any{"server": nil, "port": nil, "tls": nil, "fingerprint": nil, "path": nil}, func(d, tls map[string]any) bool {
			return d["server"] == "up.example" && d["server_port"] == float64(443) && d["path"] == "/u" && tls["enabled"] == true
		}},
		"own server and reality": {map[string]any{
			"server": "dl.example", "port": 8443, "servername": "r.example",
			"reuse-settings": map[string]any{"max-connections": "3"},
			"reality-opts":   map[string]any{"public-key": "jNXHt1yRo0vDuchQlIP6Z0ZvjT3KtzVI-T4E7RoLJS0", "short-id": "ab12", "support-x25519mlkem768": false},
		}, func(d, tls map[string]any) bool {
			r, _ := tls["reality"].(map[string]any)
			x, _ := d["xmux"].(map[string]any)
			return d["server"] == "dl.example" && d["server_port"] == float64(8443) && d["path"] == "/u" &&
				tls["server_name"] == "r.example" && r["enabled"] == true && r["support_x25519mlkem768"] == false &&
				x["max_connections"] == "3" && x["max_concurrency"] == nil
		}},
	}
	for name, c := range cases {
		ob, err := mapClashVless(clashXHTTPVless("packet-up", c.ds))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		d, _ := xhttpTransport(t, ob.Outbound)["download"].(map[string]any)
		tls, _ := d["tls"].(map[string]any)
		if !c.want(d, tls) {
			t.Errorf("%s: download = %v", name, d)
		}
	}
}

func TestClashXHTTPDownloadSettingsRejected(t *testing.T) {
	for name, node := range map[string]map[string]any{
		"stream-one": clashXHTTPVless("stream-one", map[string]any{"path": "/d"}),
		"ech":        clashXHTTPVless("packet-up", map[string]any{"ech-opts": map[string]any{"enable": true}}),
		"cert pin":   clashXHTTPVless("packet-up", map[string]any{"fingerprint": "ab"}),
		"bad port":   clashXHTTPVless("packet-up", map[string]any{"port": 0}),
		"not a map":  clashXHTTPVless("packet-up", nil),
		"reality no tls": clashXHTTPVless("packet-up", map[string]any{"tls": false,
			"reality-opts": map[string]any{"public-key": "jNXHt1yRo0vDuchQlIP6Z0ZvjT3KtzVI-T4E7RoLJS0"}}),
	} {
		if name == "not a map" {
			node["xhttp-opts"].(map[string]any)["download-settings"] = "x"
		}
		if _, err := mapClashVless(node); err == nil || !strings.Contains(err.Error(), "download-settings") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}
