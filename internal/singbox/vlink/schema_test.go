package vlink

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/singbox/installer"
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
	for _, v := range all {
		if _, ok := v["properties"]; ok {
			return v
		}
	}
	return nil
}

// checkKeys walks the value against the schema and reports keys the schema does
// not declare. Types and enums are deliberately not checked: those the option
// layer enforces, and sing-box check catches them on the device.
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
