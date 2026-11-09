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
