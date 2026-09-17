package vlink

import "testing"

// Без этого фича не работает вовсе: подписка, где ВСЕ узлы — hysteria, до
// ParseXrayBody не доходит. service.go выбирает разбор по IsXrayJSON и на
// «нет» отвечает «тело выглядит как JSON, но не похоже на Xray config».
func TestIsXrayJSON_Hysteria(t *testing.T) {
	cases := map[string][]byte{
		"массив элементов с remarks": []byte(xrayHysteria2Body),
		"один объект с outbounds": []byte(`{"outbounds":[{"protocol":"hysteria",
			"settings":{"address":"h.example.net","port":443,"version":2},
			"streamSettings":{"network":"hysteria","hysteriaSettings":{"version":2,"auth":"pw"}}}]}`),
		"голый массив аутбаундов": []byte(`[{"protocol":"hysteria",
			"settings":{"address":"h.example.net","port":443,"version":2},
			"streamSettings":{"network":"hysteria","hysteriaSettings":{"version":2,"auth":"pw"}}}]`),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if !IsXrayJSON(body) {
				t.Fatal("IsXrayJSON = false — подписка уйдёт в разбор share-ссылок и пропадёт целиком")
			}
			if res := ParseXrayBody(body); len(res.Outbounds) != 1 {
				t.Fatalf("outbounds = %d, want 1 (errors: %v)", len(res.Outbounds), res.Errors)
			}
		})
	}
}

// Битый блок ОДНОГО узла не должен уносить всю подписку: json.Unmarshal в
// типизированный массив — всё или ничего, поэтому разбор таких блоков ленивый.
func TestParseXrayBody_BrokenBlockDoesNotKillBody(t *testing.T) {
	body := []byte(`[
		{"remarks":"ok","outbounds":[{"protocol":"trojan","settings":{"servers":[{"address":"198.51.100.2","port":443,"password":"p"}]}}]},
		{"remarks":"broken","outbounds":[{"protocol":"hysteria",
			"settings":{"address":"h.example.net","port":443,"version":2},
			"streamSettings":{"network":"hysteria","hysteriaSettings":{"version":2,"auth":"pw"},
				"finalmask":"не объект"}}]}
	]`)

	res := ParseXrayBody(body)
	if len(res.Outbounds) != 1 {
		t.Fatalf("outbounds = %d, want 1 — исправный узел обязан уцелеть (errors: %v)", len(res.Outbounds), res.Errors)
	}
	if len(res.Errors) != 1 {
		t.Fatalf("errors = %v, want 1 — битый узел обязан назвать причину", res.Errors)
	}
}

// Тот же класс: чужому протоколу finalmask ничего не должен ломать — Xray
// кладёт его в streamSettings любого транспорта.
func TestParseXrayBody_BrokenFinalMaskOnOtherProtocol(t *testing.T) {
	body := []byte(`[{"remarks":"v","outbounds":[{"protocol":"trojan",
		"settings":{"servers":[{"address":"198.51.100.2","port":443,"password":"p"}]},
		"streamSettings":{"network":"tcp","finalmask":"не объект"}}]}]`)

	res := ParseXrayBody(body)
	if len(res.Outbounds) != 1 {
		t.Fatalf("outbounds = %d, want 1 (errors: %v)", len(res.Outbounds), res.Errors)
	}
}
