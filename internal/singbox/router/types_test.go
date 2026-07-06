package router

import (
	"encoding/json"
	"strings"
	"testing"
)

// Разбор legacy download_detour (sing-box <1.14) нормализуется в
// http_client.detour — старые слоты и API-клиенты продолжают работать.
func TestRuleSetUnmarshalLegacyDownloadDetour(t *testing.T) {
	var rs RuleSet
	err := json.Unmarshal([]byte(`{"tag":"geo","type":"remote","url":"https://example.com/geo.srs","download_detour":"vpn1"}`), &rs)
	if err != nil {
		t.Fatal(err)
	}
	if rs.HTTPClient == nil || rs.HTTPClient.Detour != "vpn1" {
		t.Fatalf("http_client = %+v, want detour vpn1", rs.HTTPClient)
	}
	if rs.DownloadDetourTag() != "vpn1" {
		t.Fatalf("DownloadDetourTag = %q", rs.DownloadDetourTag())
	}
}

// http_client в форме объекта побеждает legacy download_detour (upstream
// считает их конфликтом — мы отдаём приоритет новой форме).
func TestRuleSetUnmarshalHTTPClientWinsOverLegacy(t *testing.T) {
	var rs RuleSet
	err := json.Unmarshal([]byte(`{"tag":"geo","type":"remote","url":"https://example.com/geo.srs","download_detour":"old","http_client":{"detour":"new"}}`), &rs)
	if err != nil {
		t.Fatal(err)
	}
	if rs.DownloadDetourTag() != "new" {
		t.Fatalf("DownloadDetourTag = %q, want new", rs.DownloadDetourTag())
	}
}

// download_detour:"direct" схлопывается в default-клиент (nil): detour на
// пустой direct-outbound sing-box ≥1.14 отвергает на dial.
func TestRuleSetUnmarshalDirectDetourCollapses(t *testing.T) {
	for _, in := range []string{
		`{"tag":"geo","type":"remote","url":"https://e/x.srs","download_detour":"direct"}`,
		`{"tag":"geo","type":"remote","url":"https://e/x.srs","http_client":{"detour":"direct"}}`,
		`{"tag":"geo","type":"remote","url":"https://e/x.srs","http_client":{}}`,
	} {
		var rs RuleSet
		if err := json.Unmarshal([]byte(in), &rs); err != nil {
			t.Fatal(err)
		}
		if rs.HTTPClient != nil {
			t.Errorf("input %s: http_client = %+v, want nil", in, rs.HTTPClient)
		}
	}
}

// Наружу download_detour больше не эмитится — только http_client.
func TestRuleSetMarshalEmitsHTTPClientOnly(t *testing.T) {
	rs := RuleSet{Tag: "geo", Type: "remote", URL: "https://example.com/geo.srs", HTTPClient: &RuleSetHTTPClient{Detour: "vpn1"}}
	out, err := json.Marshal(rs)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "download_detour") {
		t.Fatalf("marshal emits legacy download_detour: %s", out)
	}
	if !strings.Contains(string(out), `"http_client":{"detour":"vpn1"}`) {
		t.Fatalf("marshal missing http_client: %s", out)
	}

	// Roundtrip через legacy-вход тоже не должен вернуть download_detour.
	var legacy RuleSet
	if err := json.Unmarshal([]byte(`{"tag":"geo","type":"remote","url":"https://e/x.srs","download_detour":"vpn1"}`), &legacy); err != nil {
		t.Fatal(err)
	}
	out2, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out2), "download_detour") {
		t.Fatalf("legacy roundtrip re-emits download_detour: %s", out2)
	}
}

// Строковая форма http_client (тег top-level http_clients) сохраняется
// при roundtrip и не считается detour-ссылкой на outbound.
func TestRuleSetHTTPClientStringFormRoundtrip(t *testing.T) {
	var rs RuleSet
	if err := json.Unmarshal([]byte(`{"tag":"geo","type":"remote","url":"https://e/x.srs","http_client":"my-client"}`), &rs); err != nil {
		t.Fatal(err)
	}
	if rs.HTTPClient == nil || rs.HTTPClient.Tag != "my-client" {
		t.Fatalf("http_client = %+v, want tag my-client", rs.HTTPClient)
	}
	if rs.DownloadDetourTag() != "" {
		t.Fatalf("string form must not report a detour, got %q", rs.DownloadDetourTag())
	}
	out, err := json.Marshal(rs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"http_client":"my-client"`) {
		t.Fatalf("string form lost on marshal: %s", out)
	}
	// Переименование outbound не должно затирать именованный клиент.
	rs.setDownloadDetourTag("vpn2")
	if rs.HTTPClient.Tag != "my-client" {
		t.Fatalf("setDownloadDetourTag overwrote named client: %+v", rs.HTTPClient)
	}
}
