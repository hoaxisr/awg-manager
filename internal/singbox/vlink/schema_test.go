package vlink

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/installer"
	"github.com/hoaxisr/awg-manager/internal/singbox/router"
)

// The schema is generated from the fork we pin, by scripts/regen-singbox-schema.sh.
// It is the only machine-readable statement of which keys that build accepts —
// #806 was a field silently lost on a rebase, and nothing here would have noticed.
const schemaPath = "testdata/singbox-schema.json"

// schemaDoc is a loaded schema plus its $defs, for resolving $ref.
type schemaDoc struct {
	defs map[string]any
}

func loadSchema(t *testing.T) (*schemaDoc, map[string]any) {
	t.Helper()
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read schema: %v (run scripts/regen-singbox-schema.sh)", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	// Версию штампует regen-singbox-schema.sh. Без этой сверки забытый regen
	// при бампе оставил бы тест проверять старый контракт, и поле, удалённое
	// форком, прошло бы зелёным — ровно то, ради чего тест и заведён (#806).
	if got, _ := doc["x-singbox-version"].(string); got != installer.RequiredVersion {
		t.Fatalf("схема от версии %q, а вшита %q — прогоните scripts/regen-singbox-schema.sh",
			got, installer.RequiredVersion)
	}

	defs, _ := doc["$defs"].(map[string]any)
	if len(defs) == 0 {
		t.Fatal("schema has no $defs")
	}
	return &schemaDoc{defs: defs}, doc
}

// resolve follows a $ref chain to the node it names.
func (d *schemaDoc) resolve(node map[string]any) map[string]any {
	for i := 0; i < 10; i++ {
		ref, _ := node["$ref"].(string)
		if ref == "" {
			return node
		}
		name := strings.TrimPrefix(ref, "#/$defs/")
		next, ok := d.defs[name].(map[string]any)
		if !ok {
			return node
		}
		node = next
	}
	return node
}

// variants returns the branches of a oneOf/anyOf node, or the node itself.
func (d *schemaDoc) variants(node map[string]any) []map[string]any {
	node = d.resolve(node)
	for _, key := range []string{"oneOf", "anyOf"} {
		if list, ok := node[key].([]any); ok {
			out := make([]map[string]any, 0, len(list))
			for _, v := range list {
				if m, ok := v.(map[string]any); ok {
					out = append(out, d.resolve(m))
				}
			}
			return out
		}
	}
	return []map[string]any{node}
}

// pickVariant chooses the branch whose discriminator matches the value, so the
// error message names the real offender instead of every branch at once.
func (d *schemaDoc) pickVariant(node map[string]any, value map[string]any) map[string]any {
	all := d.variants(node)
	for _, v := range all {
		props, _ := v["properties"].(map[string]any)
		typeNode, _ := props["type"].(map[string]any)
		if konst, ok := typeNode["const"]; ok && konst == value["type"] {
			return v
		}
	}
	// Отсутствие "type" в значении — это sing-box'овский вариант "default", а
	// не произвольная первая ветка с properties.
	if _, hasType := value["type"]; !hasType {
		for _, v := range all {
			props, _ := v["properties"].(map[string]any)
			typeNode, _ := props["type"].(map[string]any)
			if konst, ok := typeNode["const"]; ok && konst == "default" {
				return v
			}
			if enum, ok := typeNode["enum"].([]any); ok {
				for _, e := range enum {
					if e == "default" {
						return v
					}
				}
			}
		}
	}
	for _, v := range all {
		if _, ok := v["properties"]; ok {
			return v
		}
	}
	return nil
}

// scalarJSONType maps a decoded JSON scalar to its JSON Schema type name.
// Returns "" for values checkKeys does not scalar-check (nil/JSON null).
func scalarJSONType(value any) string {
	switch value.(type) {
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		return "number"
	}
	return ""
}

// schemaTypeMatches reports whether a schema "type" accepts a decoded value of
// jsonType. "integer" accepts a float64 too — encoding/json has no separate
// integer kind, every JSON number decodes to float64.
func schemaTypeMatches(schemaType, jsonType string) bool {
	return schemaType == jsonType || (jsonType == "number" && schemaType == "integer")
}

// scalarAllowed reports whether node's schema (directly, or via one branch of
// its oneOf/anyOf) accepts a scalar of jsonType. A branch with no "type" and
// no "properties" is untyped/free-form and accepts any scalar; a branch with
// "properties" but no "type" is an implicit object and does not.
func (d *schemaDoc) scalarAllowed(node map[string]any, jsonType string) bool {
	for _, variant := range d.variants(node) {
		if schemaType, ok := variant["type"].(string); ok {
			if schemaTypeMatches(schemaType, jsonType) {
				return true
			}
			continue
		}
		if _, isObject := variant["properties"]; !isObject {
			return true
		}
	}
	return false
}

// checkKeys walks the value against the schema and reports keys the schema does
// not declare. Types and enums are deliberately not checked: those the option
// layer enforces, and sing-box check catches them on the device. The one
// exception is a scalar checked against an object-only schema node (see the
// default case below) — that mismatch means the value landed in the wrong
// slot entirely (e.g. a plain string where the schema requires an object),
// not a fine-grained type/enum nuance.
func (d *schemaDoc) checkKeys(t *testing.T, path string, node map[string]any, value any) {
	t.Helper()
	switch v := value.(type) {
	case map[string]any:
		variant := d.pickVariant(node, v)
		if variant == nil {
			return // not an object in the schema (free-form map, e.g. headers)
		}
		props, _ := variant["properties"].(map[string]any)
		if props == nil {
			return
		}
		if extra, _ := variant["additionalProperties"].(bool); extra {
			return
		}
		for key, child := range v {
			childNode, ok := props[key].(map[string]any)
			if !ok {
				if _, free := variant["additionalProperties"].(map[string]any); free {
					continue
				}
				t.Errorf("key %q is not in the sing-box schema", path+"."+key)
				continue
			}
			d.checkKeys(t, path+"."+key, childNode, child)
		}
	case []any:
		items, _ := d.resolve(node)["items"].(map[string]any)
		if items == nil {
			return
		}
		for i, item := range v {
			d.checkKeys(t, fmt.Sprintf("%s[%d]", path, i), items, item)
		}
	default:
		jsonType := scalarJSONType(v)
		if jsonType == "" {
			return // null, or a Go type json.Unmarshal never produces here
		}
		if !d.scalarAllowed(node, jsonType) {
			t.Errorf("%s: schema expects %v, got %T", path, node, value)
		}
	}
}

// Every outbound our parsers emit must consist of keys the pinned sing-box
// declares. A key we invent, or one the fork renamed under us, fails here
// instead of on the router.
func TestParsedOutboundsMatchSchema(t *testing.T) {
	doc, root := loadSchema(t)
	outboundsNode, _ := root["properties"].(map[string]any)
	arrayNode, _ := outboundsNode["outbounds"].(map[string]any)
	itemsNode, _ := arrayNode["items"].(map[string]any)
	if itemsNode == nil {
		t.Fatal("schema has no outbounds.items")
	}

	links := map[string]string{
		"vless reality xhttp + extra": issue797Link,
		"vless xhttp no extra":        "vless://00000000-1111-2222-3333-444444444444@example.com:443?type=xhttp&mode=stream-up&path=/x&host=h.example.com#h",
		"vless ws tls":                "vless://00000000-1111-2222-3333-444444444444@example.com:443?type=ws&security=tls&path=/p&host=cdn.example.com&sni=foo.com&fp=chrome&bind_interface=nwg0#a",
		"vless grpc reality":          "vless://00000000-1111-2222-3333-444444444444@example.com:443?type=grpc&security=reality&serviceName=svc&pbk=jNXHt1yRo0vDuchQlIP6Z0ZvjT3KtzVI-T4E7RoLJS0&sid=ab12&fp=chrome&flow=xtls-rprx-vision#b",
		"vless httpupgrade":           "vless://00000000-1111-2222-3333-444444444444@example.com:443?type=httpupgrade&path=/u&host=h.example.com#c",
		"trojan ws tls":               "trojan://secret@example.com:443?type=ws&security=tls&path=/t&sni=foo.com#d",
		"shadowsocks":                 "ss://YWVzLTI1Ni1nY206c2VjcmV0@example.com:8388#e",
		"hysteria2":                   "hysteria2://secret@example.com:443?sni=foo.com&obfs=salamander&obfs-password=p#f",
		"socks5":                      "socks://user:pass@example.com:1080#g",
		"mieru":                       "mierus://user:pass@example.com?port=2999&port=3000-3010&protocol=TCP&multiplexing=MULTIPLEXING_LOW&profile=p#i",
		"naive":                       "naive+https://user:pass@example.com:443#j",
	}
	for name, link := range links {
		t.Run(name, func(t *testing.T) {
			p, err := ParseLink(link)
			if err != nil {
				t.Fatalf("ParseLink: %v", err)
			}
			var ob map[string]any
			if err := json.Unmarshal(p.Outbound, &ob); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			doc.checkKeys(t, "outbound", itemsNode, ob)
		})
	}
}

// Материализованная форма router/fakeip-слота обязана состоять из ключей,
// которые объявляет вшитый sing-box. Раньше проверялись только outbounds, и
// пропажа download_detour из схемы 1.14 прошла незамеченной.
func TestMaterializedRouterConfigMatchesSchema(t *testing.T) {
	doc, root := loadSchema(t)
	rootProps, _ := root["properties"].(map[string]any)

	sample := router.RouterConfig{
		Inbounds: []router.Inbound{
			{Type: "tproxy", Tag: "tproxy-in", Listen: "127.0.0.1", ListenPort: 51281, Network: "udp",
				UDPFragment: true, UDPTimeout: "5m0s", UDPNATMax: 4096},
			{Type: "tun", Tag: "tun-in", InterfaceName: "opkgtun0", Address: []string{"172.18.0.1/30"}, MTU: 1500,
				AutoRoute: new(bool), AutoRedirect: new(bool), StrictRoute: new(bool), Stack: "gvisor",
				UDPTimeout: "5m0s", UDPNATMax: 4096},
		},
		Outbounds: []router.Outbound{{Type: "direct", Tag: "direct"}},
		HTTPClients: []router.HTTPClient{
			{Tag: "rs-download", Detour: "direct"},
			{Tag: "rs-direct:direct"},
		},
		DNS: router.DNS{
			Servers:  []router.DNSServer{{Tag: "real", Type: "udp", Server: "1.1.1.1"}},
			Final:    "real",
			Strategy: "prefer_ipv4",
			Timeout:  "5s",
		},
		Route: router.Route{
			RuleSet: []router.RuleSet{
				{Tag: "geo", Type: "remote", Format: "binary", URL: "https://x/geo.srs",
					UpdateInterval: "24h", HTTPClient: &router.RuleSetHTTPClient{Detour: "direct"}},
				{Tag: "geo-direct", Type: "remote", Format: "binary", URL: "https://x/geo-direct.srs",
					UpdateInterval: "24h", HTTPClient: &router.RuleSetHTTPClient{Ref: "rs-direct:direct"}},
			},
			Rules: []router.Rule{{SourceMACAddress: []string{"aa:bb:cc:dd:ee:ff"}, SourceIPCIDR: []string{"192.168.1.0/24"},
				RuleSet: []string{"geo"}, Action: "route", Outbound: "direct"}},
			Final:                 "direct",
			DefaultHTTPClient:     "rs-download",
			DefaultDomainResolver: &router.DomainResolver{Server: "real"},
		},
		Experimental: &router.Experimental{CacheFile: &router.CacheFile{Enabled: true, StoreFakeIP: true, StoreDNS: true, Path: "/tmp/c.db"}},
	}
	raw, err := json.Marshal(sample)
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	for key, value := range cfg {
		node, ok := rootProps[key].(map[string]any)
		if !ok {
			t.Errorf("top-level key %q is not in the sing-box schema", key)
			continue
		}
		doc.checkKeys(t, key, node, value)
	}
}

// F115(a): checkKeys раньше проверял только map/slice-узлы — скаляр молча
// проходил ЛЮБУЮ схему, даже объектную. HTTPClientReference — anyOf со
// string-веткой (`rs-direct:<X>` из ruleset_materializer.go), поэтому строка
// обязана пройти; HTTPClient — чистый объект без anyOf, и строка на его месте
// обязана быть отклонена. checkKeys репортит через t.Errorf, поэтому
// негативный случай проверяется через тот же предикат (scalarAllowed),
// который checkKeys вызывает перед Errorf, — так тест не зависит от
// намеренно проваленного sub-теста.
func TestCheckKeys_ScalarAgainstAnyOf(t *testing.T) {
	doc, _ := loadSchema(t)

	ref := map[string]any{"$ref": "#/$defs/HTTPClientReference"}
	doc.checkKeys(t, "http_client", ref, "rs-direct:direct") // не должен звать t.Errorf

	obj := map[string]any{"$ref": "#/$defs/HTTPClient"}
	if doc.scalarAllowed(obj, "string") {
		t.Error("HTTPClient — объект без anyOf, строка на его месте обязана быть отклонена")
	}
}
