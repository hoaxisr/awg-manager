package vlink

import (
	"encoding/json"
	"fmt"
	"testing"
)

// F778: узел за CDN — адрес в server, домен в SNI, host не задан. Xray шлёт
// Host = SNI (ws, httpupgrade, xhttp), mihomo — servername только у xhttp,
// у ws и httpupgrade — адрес сервера.
func TestEmptyHostDefault(t *testing.T) {
	const uuid = "b831381d-6324-4d53-ad4f-8cda48b30811"
	hostOf := func(t *testing.T, raw json.RawMessage) any {
		t.Helper()
		var ob map[string]any
		if err := json.Unmarshal(raw, &ob); err != nil {
			t.Fatal(err)
		}
		tr, _ := ob["transport"].(map[string]any)
		if h, ok := tr["headers"].(map[string]any); ok {
			return h["Host"]
		}
		return tr["host"]
	}
	for _, network := range []string{"ws", "httpupgrade", "xhttp"} {
		t.Run(network, func(t *testing.T) {
			link, err := ParseLink(fmt.Sprintf("vless://%s@1.2.3.4:443?type=%s&path=/p&security=tls&sni=cdn.example#a", uuid, network))
			if err != nil {
				t.Fatal(err)
			}
			if got := hostOf(t, link.Outbound); got != "cdn.example" {
				t.Errorf("link: host=%v, want SNI", got)
			}
			plain, err := ParseLink(fmt.Sprintf("vless://%s@1.2.3.4:80?type=%s&path=/p#a", uuid, network))
			if err != nil {
				t.Fatal(err)
			}
			if got := hostOf(t, plain.Outbound); got != "1.2.3.4" {
				t.Errorf("link without tls: host=%v, want address", got)
			}

			settings := fmt.Sprintf(`"%sSettings":{"path":"/p"}`, network)
			res := ParseXrayBody([]byte(fmt.Sprintf(`{"outbounds":[{"protocol":"vless","tag":"x",
				"settings":{"vnext":[{"address":"1.2.3.4","port":443,"users":[{"id":"%s"}]}]},
				"streamSettings":{"network":"%s","security":"tls","tlsSettings":{"serverName":"cdn.example"},%s}}]}`, uuid, network, settings)))
			if len(res.Outbounds) != 1 {
				t.Fatalf("xray: %v", res.Errors)
			}
			if got := hostOf(t, res.Outbounds[0].Outbound); got != "cdn.example" {
				t.Errorf("xray: host=%v, want SNI", got)
			}

			p := map[string]any{"name": "c", "type": "vless", "server": "1.2.3.4", "port": 443, "uuid": uuid,
				"tls": true, "servername": "cdn.example", "network": "ws",
				"ws-opts": map[string]any{"path": "/p", "v2ray-http-upgrade": network == "httpupgrade"}}
			want := "1.2.3.4"
			if network == "xhttp" {
				p["network"] = "xhttp"
				p["xhttp-opts"] = map[string]any{"path": "/p"}
				want = "cdn.example"
			}
			ob, err := mapClashVless(p)
			if err != nil {
				t.Fatal(err)
			}
			if got := hostOf(t, ob.Outbound); got != want {
				t.Errorf("clash: host=%v, want %s", got, want)
			}
		})
	}
	reality, err := ParseLink("vless://" + uuid + "@1.2.3.4:443?type=xhttp&path=/p&security=reality&sni=r.example&pbk=jNXHt1yRo0vDuchQlIP6Z0ZvjT3KtzVI-T4E7RoLJS0&sid=ab12#r")
	if err != nil {
		t.Fatal(err)
	}
	if got := hostOf(t, reality.Outbound); got != "r.example" {
		t.Errorf("reality xhttp: host=%v, want SNI", got)
	}
}
