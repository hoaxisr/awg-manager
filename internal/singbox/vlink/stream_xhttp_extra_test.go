package vlink

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

// issue #797: xmux inside ?extra= was dropped, so every parallel stream opened
// its own REALITY handshake.
func TestBuildStreamFromQuery_XHTTPExtraXmux(t *testing.T) {
	q := parseQuery(t, "type=xhttp&extra="+
		`%7B%22xmux%22%3A%7B%22maxConcurrency%22%3A%2232-64%22%2C%22cMaxReuseTimes%22%3A0%2C%22hMaxRequestTimes%22%3A%22600-900%22%2C%22hMaxReusableSecs%22%3A%221800-3000%22%2C%22hKeepAlivePeriod%22%3A0%7D%7D`)
	s, err := BuildStreamFromQuery(q, "example.com")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	out := map[string]any{}
	s.MergeIntoOutbound(out)
	tr, _ := out["transport"].(map[string]any)
	xmux, ok := tr["xmux"].(map[string]any)
	if !ok {
		t.Fatalf("no xmux in transport: %v", tr)
	}
	want := map[string]any{
		"max_concurrency":     "32-64",
		"c_max_reuse_times":   float64(0),
		"h_max_request_times": "600-900",
		"h_max_reusable_secs": "1800-3000",
		"h_keep_alive_period": float64(0),
	}
	got, _ := json.Marshal(xmux)
	wantJSON, _ := json.Marshal(want)
	if string(got) != string(wantJSON) {
		t.Errorf("xmux=%s, want %s", got, wantJSON)
	}
}

func TestBuildStreamFromQuery_XHTTPExtraScalars(t *testing.T) {
	extra := `{"headers":{"X-Foo":"bar"},"noGRPCHeader":false,"noSSEHeader":true,` +
		`"scMaxEachPostBytes":1000000,"scMinPostsIntervalMs":30,"scMaxBufferedPosts":30,` +
		`"scStreamUpServerSecs":"20-80","xPaddingBytes":"200-800"}`
	q := parseQuery(t, "type=xhttp&extra="+urlEscape(extra))
	s, err := BuildStreamFromQuery(q, "example.com")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	out := map[string]any{}
	s.MergeIntoOutbound(out)
	tr, _ := out["transport"].(map[string]any)

	// x_padding_bytes from the link must win over the built-in default.
	if tr["x_padding_bytes"] != "200-800" {
		t.Errorf("x_padding_bytes=%v, want 200-800", tr["x_padding_bytes"])
	}
	if tr["no_sse_header"] != true {
		t.Errorf("no_sse_header=%v, want true", tr["no_sse_header"])
	}
	if tr["no_grpc_header"] != false {
		t.Errorf("no_grpc_header=%v, want false", tr["no_grpc_header"])
	}
	if tr["sc_max_each_post_bytes"] != float64(1000000) {
		t.Errorf("sc_max_each_post_bytes=%v", tr["sc_max_each_post_bytes"])
	}
	if tr["sc_min_posts_intervals_ms"] != nil {
		t.Errorf("unexpected typo key emitted: %v", tr)
	}
	if tr["sc_min_posts_interval_ms"] != float64(30) {
		t.Errorf("sc_min_posts_interval_ms=%v", tr["sc_min_posts_interval_ms"])
	}
	if tr["sc_max_buffered_posts"] != float64(30) {
		t.Errorf("sc_max_buffered_posts=%v", tr["sc_max_buffered_posts"])
	}
	if tr["sc_stream_up_server_secs"] != "20-80" {
		t.Errorf("sc_stream_up_server_secs=%v", tr["sc_stream_up_server_secs"])
	}
	hdr, ok := tr["headers"].(map[string]string)
	if !ok || hdr["X-Foo"] != "bar" {
		t.Errorf("headers=%#v", tr["headers"])
	}
}

// The option layer refuses max_connections together with max_concurrency, and
// that refusal kills the whole config, not just this outbound. Xray's own
// semantics: concurrency wins.
func TestBuildStreamFromQuery_XHTTPExtraXmuxConflict(t *testing.T) {
	cases := []struct {
		name       string
		xmux       string
		wantConcur any
		wantConns  any
	}{
		{"both set", `{"maxConcurrency":"32-64","maxConnections":4}`, "32-64", nil},
		{"zero connections kept", `{"maxConcurrency":"32-64","maxConnections":0}`, "32-64", float64(0)},
		{"connections only", `{"maxConnections":4}`, nil, float64(4)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := parseQuery(t, "type=xhttp&extra="+urlEscape(`{"xmux":`+tc.xmux+`}`))
			s, err := BuildStreamFromQuery(q, "example.com")
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			out := map[string]any{}
			s.MergeIntoOutbound(out)
			xmux, _ := out["transport"].(map[string]any)["xmux"].(map[string]any)
			if xmux["max_concurrency"] != tc.wantConcur {
				t.Errorf("max_concurrency=%v, want %v", xmux["max_concurrency"], tc.wantConcur)
			}
			if xmux["max_connections"] != tc.wantConns {
				t.Errorf("max_connections=%v, want %v", xmux["max_connections"], tc.wantConns)
			}
		})
	}
}

// The link from issue #797, placeholder values.
const issue797Link = "vless://00000000-1111-2222-3333-444444444444@example.com:443?type=xhttp&security=reality&encryption=none&pbk=REPLACE_WITH_PUBLIC_KEY&sid=0123abcd&fp=chrome&sni=example.com&host=example.com&path=%2Fyour-path&mode=auto&spx=%2F&extra=%7B%22headers%22%3A%7B%7D%2C%22noGRPCHeader%22%3Afalse%2C%22scMaxEachPostBytes%22%3A1000000%2C%22scMinPostsIntervalMs%22%3A30%2C%22xPaddingBytes%22%3A%22100-1000%22%2C%22xmux%22%3A%7B%22cMaxReuseTimes%22%3A0%2C%22hKeepAlivePeriod%22%3A0%2C%22hMaxRequestTimes%22%3A%22600-900%22%2C%22hMaxReusableSecs%22%3A%221800-3000%22%2C%22maxConcurrency%22%3A%2232-64%22%2C%22maxConnections%22%3A0%7D%7D#Example-XHTTP-Reality"

func TestParseLink_XHTTPExtraEndToEnd(t *testing.T) {
	p, err := ParseLink(issue797Link)
	if err != nil {
		t.Fatalf("ParseLink: %v", err)
	}
	var ob map[string]any
	if err := json.Unmarshal(p.Outbound, &ob); err != nil {
		t.Fatalf("unmarshal outbound: %v", err)
	}
	tr, _ := ob["transport"].(map[string]any)
	xmux, ok := tr["xmux"].(map[string]any)
	if !ok {
		t.Fatalf("no xmux: %s", p.Outbound)
	}
	if xmux["max_concurrency"] != "32-64" {
		t.Errorf("max_concurrency=%v", xmux["max_concurrency"])
	}
	if tr["sc_max_each_post_bytes"] != float64(1000000) {
		t.Errorf("sc_max_each_post_bytes=%v", tr["sc_max_each_post_bytes"])
	}
	// "headers":{} carries nothing and the option layer wants it absent.
	if _, present := tr["headers"]; present {
		t.Errorf("empty headers emitted: %v", tr["headers"])
	}
}

// A malformed or hostile extra must not take the link — or the config — down.
func TestBuildStreamFromQuery_XHTTPExtraJunk(t *testing.T) {
	for _, extra := range []string{"not-json", "[1,2]", `{"xmux":"nope"}`,
		`{"xPaddingBytes":true,"scMaxBufferedPosts":"x","headers":{"Host":"a","X-Ok":1}}`,
		`{"xmux":{"maxConcurrency":"a-b","unknownKey":1}}`} {
		q := parseQuery(t, "type=xhttp&extra="+urlEscape(extra))
		s, err := BuildStreamFromQuery(q, "example.com")
		if err != nil {
			t.Fatalf("extra=%s: %v", extra, err)
		}
		out := map[string]any{}
		s.MergeIntoOutbound(out)
		tr, _ := out["transport"].(map[string]any)
		want := map[string]any{"type": "xhttp", "host": "example.com", "x_padding_bytes": "100-1000"}
		got, _ := json.Marshal(tr)
		wantJSON, _ := json.Marshal(want)
		if string(got) != string(wantJSON) {
			t.Errorf("extra=%s produced %s, want %s", extra, got, wantJSON)
		}
	}
}

// xbadoption.Range refuses from > to and decodes into int32; a value the
// option layer rejects would take the whole config down, so it never gets
// emitted.
func TestBuildStreamFromQuery_XHTTPExtraOutOfRange(t *testing.T) {
	for _, extra := range []string{
		`{"xPaddingBytes":"64-32"}`,
		`{"xPaddingBytes":1.5}`,
		`{"xPaddingBytes":3000000000}`,
		`{"xPaddingBytes":"1-3000000000"}`,
		`{"xPaddingBytes":-5}`,
	} {
		q := parseQuery(t, "type=xhttp&extra="+urlEscape(extra))
		s, err := BuildStreamFromQuery(q, "example.com")
		if err != nil {
			t.Fatalf("extra=%s: %v", extra, err)
		}
		out := map[string]any{}
		s.MergeIntoOutbound(out)
		tr, _ := out["transport"].(map[string]any)
		if tr["x_padding_bytes"] != "100-1000" {
			t.Errorf("extra=%s: x_padding_bytes=%v, want the default", extra, tr["x_padding_bytes"])
		}
	}
}

func urlEscape(s string) string { return url.QueryEscape(s) }

// A link imported with xmux must carry it again when shared back out (#797).
func TestEncodeOutbound_XHTTPExtraRoundTrip(t *testing.T) {
	p, err := ParseLink(issue797Link)
	if err != nil {
		t.Fatalf("ParseLink: %v", err)
	}
	link, err := EncodeOutbound(p.Outbound, "x")
	if err != nil {
		t.Fatalf("EncodeOutbound: %v", err)
	}
	back, err := ParseLink(link)
	if err != nil {
		t.Fatalf("re-parse %s: %v", link, err)
	}
	var first, second map[string]any
	_ = json.Unmarshal(p.Outbound, &first)
	_ = json.Unmarshal(back.Outbound, &second)
	got, _ := json.Marshal(second["transport"])
	want, _ := json.Marshal(first["transport"])
	if string(got) != string(want) {
		t.Errorf("transport after round-trip:\n got %s\nwant %s", got, want)
	}
}

// The option layer refuses a disabled padding outright ("x_padding_bytes
// cannot be disabled"), and that refusal takes the whole config down, so a
// zero from the link must never reach it.
func TestBuildStreamFromQuery_XHTTPExtraZeroPadding(t *testing.T) {
	for _, extra := range []string{
		`{"xPaddingBytes":"0"}`,
		`{"xPaddingBytes":0}`,
		`{"xPaddingBytes":"0-100"}`,
		`{"xPaddingBytes":"100-0"}`,
	} {
		q := parseQuery(t, "type=xhttp&extra="+urlEscape(extra))
		s, err := BuildStreamFromQuery(q, "example.com")
		if err != nil {
			t.Fatalf("extra=%s: %v", extra, err)
		}
		out := map[string]any{}
		s.MergeIntoOutbound(out)
		tr, _ := out["transport"].(map[string]any)
		if tr["x_padding_bytes"] != "100-1000" {
			t.Errorf("extra=%s: x_padding_bytes=%v, want the default", extra, tr["x_padding_bytes"])
		}
	}
}

// The option layer refuses any mode outside its four, and that refusal takes
// the whole config down — so the link is rejected instead.
func TestBuildStreamFromQuery_XHTTPMode(t *testing.T) {
	for _, mode := range []string{"auto", "packet-up", "stream-up", "stream-one", ""} {
		if _, err := BuildStreamFromQuery(parseQuery(t, "type=xhttp&mode="+mode), "example.com"); err != nil {
			t.Errorf("mode=%q rejected: %v", mode, err)
		}
	}
	if _, err := BuildStreamFromQuery(parseQuery(t, "type=xhttp&mode=oops"), "example.com"); err == nil {
		t.Error("mode=oops accepted, want an error")
	}
}

// bind_interface — настройка нашего роутера, а не свойство сервера: ссылка
// его не несёт. Импорт при этом его понимает (#709).
func TestEncodeOutbound_BindInterfaceStaysHome(t *testing.T) {
	p, err := ParseLink("vless://00000000-1111-2222-3333-444444444444@example.com:443?type=ws&bind_interface=nwg0#x")
	if err != nil {
		t.Fatalf("ParseLink: %v", err)
	}
	var imported map[string]any
	_ = json.Unmarshal(p.Outbound, &imported)
	if imported["bind_interface"] != "nwg0" {
		t.Fatalf("импорт потерял bind_interface: %v", imported)
	}
	link, err := EncodeOutbound(p.Outbound, "x")
	if err != nil {
		t.Fatalf("EncodeOutbound: %v", err)
	}
	if strings.Contains(link, "bind_interface") {
		t.Errorf("ссылка унесла привязку к чужому интерфейсу: %s", link)
	}
}

// Импорт умеет нестандартное имя заголовка early data (Clash его задаёт явно),
// а экспорт молча подменял его дефолтным — круг терял настройку.
func TestEncodeOutbound_WSEarlyDataHeader(t *testing.T) {
	cases := []struct{ header, wantParam string }{
		{"Sec-WebSocket-Protocol", ""},       // дефолт формы "?ed=" — писать незачем
		{"X-Custom-Early", "X-Custom-Early"}, // всё остальное обязано доехать
		{"", "-"},                            // early data в пути: заголовка нет
	}
	for _, tc := range cases {
		ob := map[string]any{
			"type": "vless", "server": "e.example.com", "server_port": 443,
			"uuid": "11111111-2222-3333-4444-555555555555",
			"transport": map[string]any{
				"type": "ws", "path": "/ws", "max_early_data": 2048,
			},
		}
		if tc.header != "" {
			ob["transport"].(map[string]any)["early_data_header_name"] = tc.header
		}
		raw, _ := json.Marshal(ob)
		link, err := EncodeOutbound(raw, "e")
		if err != nil {
			t.Fatalf("header=%q: %v", tc.header, err)
		}
		back, err := ParseLink(link)
		if err != nil {
			t.Fatalf("header=%q: re-parse %s: %v", tc.header, link, err)
		}
		var got map[string]any
		_ = json.Unmarshal(back.Outbound, &got)
		tr, _ := got["transport"].(map[string]any)
		if tr["max_early_data"] != float64(2048) {
			t.Errorf("header=%q: max_early_data=%v, link=%s", tc.header, tr["max_early_data"], link)
		}
		gotHeader, _ := tr["early_data_header_name"].(string)
		if gotHeader != tc.header {
			t.Errorf("header=%q: после круга %q, link=%s", tc.header, gotHeader, link)
		}
	}
}

// #908: подписка несла "scMaxEachPostBytes":"0". Xray считает нулевую верхнюю
// границу признаком «не задано» и берёт умолчание, а sing-box паникует на
// From <= 0 и уносит с собой весь процесс вместе с прокси. Переносить такое
// значение нельзя — умолчание у обоих одинаковое.
func TestParseXHTTPExtra_ZeroScRangesDropped(t *testing.T) {
	extra := `{"scMaxEachPostBytes":"0","scMinPostsIntervalMs":"0","scStreamUpServerSecs":"0",` +
		`"xPaddingBytes":"0","scMaxBufferedPosts":30,"noGRPCHeader":false}`
	got := parseXHTTPExtra(extra, "")
	for _, k := range []string{
		"sc_max_each_post_bytes",
		"sc_min_posts_interval_ms",
		"sc_stream_up_server_secs",
		"x_padding_bytes",
	} {
		if v, present := got[k]; present {
			t.Errorf("%s=%v перенесено, ожидался пропуск", k, v)
		}
	}
	// Остальное не задето.
	if got["sc_max_buffered_posts"] != float64(30) {
		t.Errorf("sc_max_buffered_posts=%v", got["sc_max_buffered_posts"])
	}
	if got["no_grpc_header"] != false {
		t.Errorf("no_grpc_header=%v", got["no_grpc_header"])
	}

	// Диапазон, НАЧИНАЮЩИЙСЯ с нуля, тоже роняет sing-box — и тоже не едет.
	if v, present := parseXHTTPExtra(`{"scMaxEachPostBytes":"0-1000"}`, "")["sc_max_each_post_bytes"]; present {
		t.Errorf("sc_max_each_post_bytes=%v перенесено", v)
	}
	// Нормальное значение переносится как было.
	if v := parseXHTTPExtra(`{"scMaxEachPostBytes":"1000-2000"}`, "")["sc_max_each_post_bytes"]; v != "1000-2000" {
		t.Errorf("sc_max_each_post_bytes=%v, want 1000-2000", v)
	}
}

// Тот же ноль, пришедший ссылкой целиком — путь, которым его принёс репортёр.
func TestParseVless_Issue908LinkDoesNotCarryZeroRange(t *testing.T) {
	extra := url.QueryEscape(`{"scMaxEachPostBytes":"0","xmux":{"maxConcurrency":"0","maxConnections":"3"}}`)
	link := "vless://11111111-2222-3333-4444-555555555555@188.68.218.110:443" +
		"?type=xhttp&mode=packet-up&path=%2Fapi%2Fv2%2F&host=forward-change.pushbyte.cc" +
		"&security=tls&sni=forward-change.pushbyte.cc&extra=" + extra + "#Germany"
	got, err := ParseLink(link)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	json.Unmarshal(got.Outbound, &ob)
	tr, _ := ob["transport"].(map[string]any)
	if v, present := tr["sc_max_each_post_bytes"]; present {
		t.Errorf("sc_max_each_post_bytes=%v перенесено в аутбаунд", v)
	}
	// xmux с нулями не трогаем: там ноль означает «без лимита» и паники нет.
	xmux, _ := tr["xmux"].(map[string]any)
	if xmux["max_concurrency"] != "0" {
		t.Errorf("xmux.max_concurrency=%v, want 0", xmux["max_concurrency"])
	}
}

// Импорт выбрасывал обфускацию паддинга и размещение session/seq/данных
// uplink, хотя форк эти поля принимает: туннель импортировался, а сервер с
// нестандартными значениями отвечал 400.
func TestParseXHTTPExtra_PlacementAndObfsFields(t *testing.T) {
	cases := []struct {
		extra, key string
		want       any
	}{
		{`{"xPaddingObfsMode":true}`, "x_padding_obfs_mode", true},
		{`{"xPaddingKey":"pad"}`, "x_padding_key", "pad"},
		{`{"xPaddingHeader":"X-Pad"}`, "x_padding_header", "X-Pad"},
		{`{"xPaddingPlacement":"cookie"}`, "x_padding_placement", "cookie"},
		{`{"xPaddingPlacement":"header"}`, "x_padding_placement", "header"},
		{`{"xPaddingPlacement":"query"}`, "x_padding_placement", "query"},
		{`{"xPaddingPlacement":"queryInHeader"}`, "x_padding_placement", "queryInHeader"},
		{`{"xPaddingMethod":"repeat-x"}`, "x_padding_method", "repeat-x"},
		{`{"xPaddingMethod":"tokenish"}`, "x_padding_method", "tokenish"},
		{`{"uplinkHTTPMethod":"PUT"}`, "uplink_http_method", "PUT"},
		{`{"sessionIDPlacement":"header"}`, "session_placement", "header"},
		{`{"sessionIDKey":"X-Sid"}`, "session_key", "X-Sid"},
		{`{"seqPlacement":"query"}`, "seq_placement", "query"},
		{`{"seqPlacement":"path"}`, "seq_placement", "path"},
		{`{"seqKey":"s"}`, "seq_key", "s"},
		{`{"uplinkDataPlacement":"body"}`, "uplink_data_placement", "body"},
		{`{"uplinkDataPlacement":"auto"}`, "uplink_data_placement", "auto"},
		{`{"uplinkDataKey":"X-D"}`, "uplink_data_key", "X-D"},
		{`{"uplinkChunkSize":"64-256"}`, "uplink_chunk_size", "64-256"},
		{`{"uplinkChunkSize":1024}`, "uplink_chunk_size", float64(1024)},
		// Старые имена Xray до #6258 и новые ведут в одно поле форка.
		{`{"sessionPlacement":"cookie"}`, "session_placement", "cookie"},
		{`{"sessionKey":"sid"}`, "session_key", "sid"},
		// Оба имени сразу: побеждает новое, как прочтёт и сам Xray.
		{`{"sessionPlacement":"cookie","sessionIDPlacement":"query"}`, "session_placement", "query"},
		{`{"sessionIDKey":"new","sessionKey":"old"}`, "session_key", "new"},
	}
	for _, tc := range cases {
		got := parseXHTTPExtra(tc.extra, "")
		if got[tc.key] != tc.want || len(got) != 1 {
			t.Errorf("extra=%s: got %v, want only %s=%v", tc.extra, got, tc.key, tc.want)
		}
	}
}

// Значение, которое форк отвергает при загрузке, роняет всю конфигурацию —
// такое поле не переносим вовсе.
func TestParseXHTTPExtra_InvalidPlacementValuesDropped(t *testing.T) {
	for _, extra := range []string{
		`{"xPaddingPlacement":"path"}`,
		`{"xPaddingPlacement":"QueryInHeader"}`,
		`{"xPaddingMethod":"random"}`,
		`{"sessionIDPlacement":"body"}`,
		`{"sessionIDPlacement":"Header"}`,
		`{"seqPlacement":"auto"}`,
		`{"uplinkDataPlacement":"path"}`,
		`{"xPaddingObfsMode":"true"}`,
		`{"xPaddingKey":""}`,
		`{"sessionIDKey":5}`,
		`{"uplinkHTTPMethod":""}`,
		`{"uplinkChunkSize":"a-b"}`,
		`{"uplinkChunkSize":-1}`,
		`{"uplinkChunkSize":"256-64"}`,
		// Новое имя с негодным значением не уступает старому: Xray старое
		// не читает вовсе.
		`{"sessionIDPlacement":"bogus","sessionPlacement":"cookie"}`,
		// У форка нет этих полей; неизвестный ключ ломает конфигурацию.
		`{"sessionIDTable":"abc","sessionIDLength":"8-16"}`,
	} {
		if got := parseXHTTPExtra(extra, "packet-up"); got != nil {
			t.Errorf("extra=%s перенесено: %v", extra, got)
		}
	}
}

// GET и данные uplink в cookie/header форк (как и Xray) допускает только в
// packet-up, иначе отвергает конфигурацию целиком.
func TestParseXHTTPExtra_PacketUpOnlyValues(t *testing.T) {
	cases := []struct {
		extra, key string
	}{
		{`{"uplinkHTTPMethod":"GET"}`, "uplink_http_method"},
		{`{"uplinkHTTPMethod":"get"}`, "uplink_http_method"},
		{`{"uplinkDataPlacement":"cookie"}`, "uplink_data_placement"},
		{`{"uplinkDataPlacement":"header"}`, "uplink_data_placement"},
	}
	for _, tc := range cases {
		for _, mode := range []string{"", "auto", "stream-up", "stream-one"} {
			if v, present := parseXHTTPExtra(tc.extra, mode)[tc.key]; present {
				t.Errorf("mode=%q extra=%s: %s=%v перенесено", mode, tc.extra, tc.key, v)
			}
		}
		if _, present := parseXHTTPExtra(tc.extra, "packet-up")[tc.key]; !present {
			t.Errorf("mode=packet-up extra=%s: %s потеряно", tc.extra, tc.key)
		}
	}
}

const placementExtra = `{"xPaddingBytes":"100-1000","xPaddingObfsMode":true,"xPaddingKey":"pad",` +
	`"xPaddingHeader":"X-Pad","xPaddingPlacement":"header","xPaddingMethod":"tokenish",` +
	`"uplinkHTTPMethod":"PUT","sessionPlacement":"cookie","sessionKey":"sid",` +
	`"seqPlacement":"query","seqKey":"sq","uplinkDataPlacement":"header",` +
	`"uplinkDataKey":"X-D","uplinkChunkSize":"64-256"}`

func checkPlacementTransport(t *testing.T, tr map[string]any) {
	t.Helper()
	want := map[string]any{
		"x_padding_obfs_mode":   true,
		"x_padding_key":         "pad",
		"x_padding_header":      "X-Pad",
		"x_padding_placement":   "header",
		"x_padding_method":      "tokenish",
		"uplink_http_method":    "PUT",
		"session_placement":     "cookie",
		"session_key":           "sid",
		"seq_placement":         "query",
		"seq_key":               "sq",
		"uplink_data_placement": "header",
		"uplink_data_key":       "X-D",
		"uplink_chunk_size":     "64-256",
	}
	for k, w := range want {
		if tr[k] != w {
			t.Errorf("%s=%v, want %v", k, tr[k], w)
		}
	}
}

// Поля переживают круг ссылка → аутбаунд → ссылка, а session уходит в
// ссылку под новыми именами Xray: старые он больше не принимает.
func TestEncodeOutbound_XHTTPPlacementRoundTrip(t *testing.T) {
	link := "vless://00000000-1111-2222-3333-444444444444@example.com:443" +
		"?type=xhttp&mode=packet-up&security=tls&sni=example.com&extra=" + urlEscape(placementExtra) + "#x"
	p, err := ParseLink(link)
	if err != nil {
		t.Fatalf("ParseLink: %v", err)
	}
	var first map[string]any
	_ = json.Unmarshal(p.Outbound, &first)
	tr, _ := first["transport"].(map[string]any)
	checkPlacementTransport(t, tr)

	out, err := EncodeOutbound(p.Outbound, "x")
	if err != nil {
		t.Fatalf("EncodeOutbound: %v", err)
	}
	u, err := url.Parse(out)
	if err != nil {
		t.Fatalf("parse %s: %v", out, err)
	}
	extra := u.Query().Get("extra")
	for _, name := range []string{`"sessionIDPlacement"`, `"sessionIDKey"`} {
		if !strings.Contains(extra, name) {
			t.Errorf("extra=%s без %s", extra, name)
		}
	}
	for _, name := range []string{`"sessionPlacement"`, `"sessionKey"`} {
		if strings.Contains(extra, name) {
			t.Errorf("extra=%s несёт старое имя %s", extra, name)
		}
	}

	back, err := ParseLink(out)
	if err != nil {
		t.Fatalf("re-parse %s: %v", out, err)
	}
	var second map[string]any
	_ = json.Unmarshal(back.Outbound, &second)
	got, _ := json.Marshal(second["transport"])
	want, _ := json.Marshal(first["transport"])
	if string(got) != string(want) {
		t.Errorf("transport after round-trip:\n got %s\nwant %s", got, want)
	}
}

// JSON Xray несёт те же поля в xhttpSettings.
func TestSetXHTTPValues_PlacementFields(t *testing.T) {
	v := url.Values{"type": {"xhttp"}}
	setXHTTPValues(v, json.RawMessage(`{"mode":"packet-up","extra":`+placementExtra+`}`))
	s, err := BuildStreamFromQuery(v, "example.com")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	out := map[string]any{}
	s.MergeIntoOutbound(out)
	tr, _ := out["transport"].(map[string]any)
	checkPlacementTransport(t, tr)
}

// mihomo несёт те же поля в xhttp-opts под kebab-case.
func TestMapClashVless_XHTTPPlacementFields(t *testing.T) {
	in := map[string]any{
		"name": "xh", "type": "vless", "server": "h.example.com", "port": 443,
		"uuid": "3a3b1c2e-9999-4321-aaaa-1234567890ab", "network": "xhttp",
		"xhttp-opts": map[string]any{
			"mode":                  "packet-up",
			"x-padding-obfs-mode":   true,
			"x-padding-key":         "pad",
			"x-padding-header":      "X-Pad",
			"x-padding-placement":   "header",
			"x-padding-method":      "tokenish",
			"uplink-http-method":    "PUT",
			"session-placement":     "cookie",
			"session-key":           "sid",
			"seq-placement":         "query",
			"seq-key":               "sq",
			"uplink-data-placement": "header",
			"uplink-data-key":       "X-D",
			"uplink-chunk-size":     "64-256",
			"session-table":         "abc",
			"session-length":        "8-16",
		},
	}
	got, err := mapClashVless(in)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var ob map[string]any
	if err := json.Unmarshal(got.Outbound, &ob); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	tr, _ := ob["transport"].(map[string]any)
	checkPlacementTransport(t, tr)
	for _, k := range []string{"session_table", "session_length"} {
		if _, present := tr[k]; present {
			t.Errorf("%s перенесено: %v", k, tr)
		}
	}
}
