package vlink

import (
	"strings"
	"testing"
)

// Номер в сообщении — человеческий, с единицы: рядом, при одиночном добавлении
// ссылки, к тому же индексу уже прибавляют единицу
// (internal/singbox/operator_tunnels.go), и два разных отсчёта в одном
// интерфейсе читаются как ошибка.
func TestParseError_NumbersFromOne(t *testing.T) {
	got := ParseError{LineIdx: 0, Scheme: "vless", Message: "boom"}.Error()
	if !strings.HasPrefix(got, "line 1 (vless)") {
		t.Errorf("Error() = %q, want «line 1»", got)
	}
}

// У подписки в формате JSON или YAML строк нет: индекс считает УЗЛЫ, и звать
// его строкой значит посылать пользователя искать не там.
func TestParseError_StructuredFormatsCountNodes(t *testing.T) {
	got := ParseError{LineIdx: 2, Scheme: "hysteria", Message: "boom", Node: true}.Error()
	if !strings.HasPrefix(got, "node 3 (hysteria)") {
		t.Errorf("Error() = %q, want «node 3»", got)
	}
}

// Текстовая подписка считает именно строки — там номер к месту.
func TestParseBatch_ReportsLines(t *testing.T) {
	res := ParseBatch([]string{"vless://bad", "ss://also-bad"})
	if len(res.Errors) != 2 {
		t.Fatalf("errors = %v", res.Errors)
	}
	if !strings.HasPrefix(res.Errors[0].Error(), "line 1 ") {
		t.Errorf("первая ошибка: %q", res.Errors[0].Error())
	}
	if res.Errors[0].Node || res.Errors[1].Node {
		t.Error("строки текстовой подписки узлами не считаются")
	}
}

// Все структурированные разборы обязаны помечать индекс как узловой.
func TestStructuredParsers_MarkNodeIndex(t *testing.T) {
	bodies := map[string]BatchResult{
		"xray": ParseXrayBody([]byte(`[{"remarks":"a","outbounds":[{"protocol":"hysteria",
			"settings":{"address":"h.example.net","port":443,"version":1},
			"streamSettings":{"network":"hysteria","hysteriaSettings":{"version":1,"auth":"p"}}}]}]`)),
		"sing-box": ParseSingboxBody([]byte(`{"outbounds":[{"type":"vless","server":"h.example.net","server_port":443}]}`)),
		"clash":    ParseClashBody([]byte("proxies:\n  - name: a\n    type: vless\n    server: h.example.net\n    port: 443\n")),
	}
	for name, res := range bodies {
		t.Run(name, func(t *testing.T) {
			if len(res.Errors) == 0 {
				t.Fatalf("фикстура без отказов: %+v", res)
			}
			for _, e := range res.Errors {
				if !e.Node {
					t.Errorf("%q помечен как строка, а это узел", e.Error())
				}
			}
		})
	}
}
