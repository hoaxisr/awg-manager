package vlink

// Тест эквивалентности входных путей: один и тот же сервер, описанный в
// разных форматах (share-ссылка, Clash YAML, Xray JSON, Amnezia vpn://,
// sing-box JSON, mieru client JSON), обязан давать одинаковый sing-box
// аутбаунд. Эталон (canonical) — авторский sing-box outbound JSON без
// "tag", проверяемый по схеме форка. Каждое известное расхождение —
// строка в testdata/equivalence_known.txt; список может только
// сокращаться: новый diff и исчезнувший diff одинаково валят тест.
//
// Законные различия и как они исключены из сравнения:
//   - tag/label (fragment ссылки, name у Clash, remarks у Xray) — ключ
//     "tag" удаляется перед сравнением;
//   - формат физически не выражает поле/протокол — у сценария просто нет
//     рендеринга для этого формата.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// eqScenario — один логический сервер во всех форматах, которые способны
// его выразить. Пустое поле = формат в сценарии не участвует.
type eqScenario struct {
	name      string
	canonical string // эталонный sing-box outbound JSON, БЕЗ "tag"
	link      string // share-ссылка
	clash     string // тело Clash-подписки (proxies: ...)
	xray      string // Xray JSON вида {"outbounds":[...]}
	amnezia   bool   // строить vpn://-рендеринг из xray (только vless)
	mieruJSON string // канонический mieru client JSON
}

var eqScenarios = []eqScenario{
	{
		name: "vless-tcp-reality-vision",
		canonical: `{
			"type": "vless",
			"server": "s1.example.com",
			"server_port": 443,
			"uuid": "11111111-2222-3333-4444-555555555555",
			"flow": "xtls-rprx-vision",
			"tls": {
				"enabled": true,
				"server_name": "cdn.example.com",
				"utls": {"enabled": true, "fingerprint": "chrome"},
				"reality": {
					"enabled": true,
					"public_key": "jNXHt1yRo0vDuchQlIP6Z0ZvjT3KtzVI-T4E7RoLJS0",
					"short_id": "ab12"
				}
			}
		}`,
		link: "vless://11111111-2222-3333-4444-555555555555@s1.example.com:443" +
			"?type=tcp&security=reality&sni=cdn.example.com" +
			"&pbk=jNXHt1yRo0vDuchQlIP6Z0ZvjT3KtzVI-T4E7RoLJS0&sid=ab12" +
			"&fp=chrome&flow=xtls-rprx-vision#s1",
		clash: `proxies:
  - name: s1
    type: vless
    server: s1.example.com
    port: 443
    uuid: 11111111-2222-3333-4444-555555555555
    flow: xtls-rprx-vision
    servername: cdn.example.com
    client-fingerprint: chrome
    reality-opts:
      public-key: jNXHt1yRo0vDuchQlIP6Z0ZvjT3KtzVI-T4E7RoLJS0
      short-id: ab12
`,
		xray: `{"outbounds":[{
			"protocol": "vless",
			"tag": "s1",
			"settings": {"vnext": [{
				"address": "s1.example.com",
				"port": 443,
				"users": [{
					"id": "11111111-2222-3333-4444-555555555555",
					"encryption": "none",
					"flow": "xtls-rprx-vision"
				}]
			}]},
			"streamSettings": {
				"network": "tcp",
				"security": "reality",
				"realitySettings": {
					"serverName": "cdn.example.com",
					"publicKey": "jNXHt1yRo0vDuchQlIP6Z0ZvjT3KtzVI-T4E7RoLJS0",
					"shortId": "ab12",
					"fingerprint": "chrome"
				}
			}
		}]}`,
		amnezia: true,
	},
}

// eqDiff — одно расхождение между эталоном и выходом пути.
type eqDiff struct {
	field string
	kind  string // "missing" | "extra" | "differs"
	want  any
	got   any
}

// diffMaps рекурсивно сравнивает want (эталон) и got. Целиком
// отсутствующее поддерево даёт ОДНУ запись на корневом узле — ledger
// остаётся читаемым.
func diffMaps(path string, want, got map[string]any, out *[]eqDiff) {
	join := func(k string) string {
		if path == "" {
			return k
		}
		return path + "." + k
	}
	for k, wv := range want {
		gv, ok := got[k]
		if !ok {
			*out = append(*out, eqDiff{field: join(k), kind: "missing", want: wv})
			continue
		}
		wm, wIsMap := wv.(map[string]any)
		gm, gIsMap := gv.(map[string]any)
		if wIsMap && gIsMap {
			diffMaps(join(k), wm, gm, out)
			continue
		}
		if !reflect.DeepEqual(wv, gv) {
			*out = append(*out, eqDiff{field: join(k), kind: "differs", want: wv, got: gv})
		}
	}
	for k, gv := range got {
		if _, ok := want[k]; !ok {
			*out = append(*out, eqDiff{field: join(k), kind: "extra", got: gv})
		}
	}
}

const knownDivergencesPath = "testdata/equivalence_known.txt"

// loadKnownDivergences читает ledger: строка = "<scenario> <format> <field> <kind>",
// пустые строки и #-комментарии пропускаются.
func loadKnownDivergences(t *testing.T) map[string]bool {
	t.Helper()
	raw, err := os.ReadFile(knownDivergencesPath)
	if err != nil {
		t.Fatalf("read known divergences: %v", err)
	}
	known := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		known[line] = true
	}
	return known
}

// scenarioRenderings собирает пары формат→вход. Рендеринг sing-box JSON
// строится из эталона механически (эталон и есть желаемый вход) — эта
// клетка проверяет детекцию, валидацию и сквозной проход raw-копии.
// Amnezia-рендеринг — обёртка vpn://base64 над Xray-фикстурой: внутренний
// формат vpn:// и есть Xray-конфиг.
func scenarioRenderings(sc eqScenario) map[string]string {
	r := map[string]string{}
	if sc.link != "" {
		r["link"] = sc.link
	}
	if sc.clash != "" {
		r["clash"] = sc.clash
	}
	if sc.xray != "" {
		r["xray"] = sc.xray
	}
	if sc.amnezia && sc.xray != "" {
		r["amnezia"] = "vpn://" + base64.RawURLEncoding.EncodeToString([]byte(sc.xray))
	}
	if sc.mieruJSON != "" {
		r["mieru-json"] = sc.mieruJSON
	}
	r["singbox"] = `{"outbounds":[` + sc.canonical + `]}`
	return r
}

// parseVia прогоняет вход через штатный парсер формата и возвращает
// нормализованный аутбаунд (без "tag"). Парс обязан пройти и дать ровно
// один аутбаунд — иначе фикстура кривая, это hard fail, не ledger.
func parseVia(t *testing.T, format, input string) map[string]any {
	t.Helper()
	var res BatchResult
	switch format {
	case "link", "amnezia":
		parsed, err := ParseLinkMany(input)
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		res.Outbounds = parsed
	case "clash":
		res = ParseClashBody([]byte(input))
	case "xray":
		res = ParseXrayBody([]byte(input))
	case "singbox":
		res = ParseSingboxBody([]byte(input))
	case "mieru-json":
		res = ParseMieruClientJSON([]byte(input))
	default:
		t.Fatalf("unknown format %q", format)
	}
	if len(res.Errors) > 0 {
		t.Fatalf("%s: parse errors: %v", format, res.Errors)
	}
	if len(res.Outbounds) != 1 {
		t.Fatalf("%s: want exactly 1 outbound, got %d", format, len(res.Outbounds))
	}
	var ob map[string]any
	if err := json.Unmarshal(res.Outbounds[0].Outbound, &ob); err != nil {
		t.Fatalf("%s: unmarshal outbound: %v", format, err)
	}
	delete(ob, "tag") // законное различие форматов
	return ob
}

// TestPathEquivalence — см. комментарий пакета в шапке файла.
func TestPathEquivalence(t *testing.T) {
	doc, root := loadSchema(t)
	outboundsNode, _ := root["properties"].(map[string]any)
	arrayNode, _ := outboundsNode["outbounds"].(map[string]any)
	itemsNode, _ := arrayNode["items"].(map[string]any)
	if itemsNode == nil {
		t.Fatal("schema has no outbounds.items")
	}

	known := loadKnownDivergences(t)
	seen := map[string]bool{}

	for _, sc := range eqScenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			var canon map[string]any
			if err := json.Unmarshal([]byte(sc.canonical), &canon); err != nil {
				t.Fatalf("canonical: %v", err)
			}
			// Эталон обязан состоять из ключей, которые знает схема форка.
			doc.checkKeys(t, "canonical", itemsNode, canon)

			for format, input := range scenarioRenderings(sc) {
				got := parseVia(t, format, input)
				var diffs []eqDiff
				diffMaps("", canon, got, &diffs)
				for _, d := range diffs {
					key := fmt.Sprintf("%s %s %s %s", sc.name, format, d.field, d.kind)
					if known[key] {
						seen[key] = true
						continue
					}
					t.Errorf("НОВОЕ расхождение: %s (want=%v got=%v)", key, d.want, d.got)
				}
			}
		})
	}

	var stale []string
	for k := range known {
		if !seen[k] {
			stale = append(stale, k)
		}
	}
	sort.Strings(stale)
	for _, k := range stale {
		t.Errorf("расхождение из ledger исчезло — удалите строку: %q", k)
	}
}
