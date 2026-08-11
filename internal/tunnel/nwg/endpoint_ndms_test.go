package nwg

import (
	"context"
	"strings"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/config"
)

func TestEndpointHostIsIPv6(t *testing.T) {
	cases := map[string]bool{
		"[2a02:6b8::feed:ff]:51820": true,
		"[::1]:1":                   true,
		"2a02:6b8::feed:ff":         true, // голый v6
		"[2a02:6b8::feed:ff]":       true, // скобки без порта
		"2a02:6b8::feed:ff:51820":   true, // небракетированный v6:port (выгрузки провайдеров)
		"[::ffff:1.2.3.4]:51820":    true, // IPv4-mapped — форма с двоеточиями, NDMS отвергает
		"1.2.3.4:51820":             false,
		"1.2.3.4":                   false,
		"vpn.example.com:51820":     false,
		"":                          false,
		"garbage":                   false,
	}
	for ep, want := range cases {
		if got := EndpointHostIsIPv6(ep); got != want {
			t.Errorf("EndpointHostIsIPv6(%q) = %v, want %v", ep, got, want)
		}
	}
}

func TestEndpointMayResolveIPv6(t *testing.T) {
	cases := map[string]bool{
		"[2a02:6b8::feed:ff]:51820": true,  // v6-литерал
		"2a02:6b8::feed:ff:51820":   true,  // небракетированный v6:port
		"[2a02::1]":                 true,  // v6 без порта — форма с двоеточиями, NDMS отвергнет
		"[::ffff:1.2.3.4]:51820":    true,  // IPv4-mapped: To4()!=nil, но NDMS отвергает — Start кладёт заглушку, boot обязан чинить
		"vpn.example.com:51820":     true,  // hostname — может резолвиться в v6 (DDNS c AAAA)
		"1.2.3.4:51820":             false, // v4-литерал — в NDMS реальный endpoint, boot no-op
		"1.2.3.4":                   false,
		"vpn.example.com":           false, // hostname без порта — стартовать нечем, boot бессилен
		"":                          false,
	}
	for ep, want := range cases {
		if got := EndpointMayResolveIPv6(ep); got != want {
			t.Errorf("EndpointMayResolveIPv6(%q) = %v, want %v", ep, got, want)
		}
	}
}

func TestCanonicalV6Endpoint(t *testing.T) {
	cases := map[string]struct {
		out string
		ok  bool
	}{
		"[2a02:6b8::1]:4433":    {"[2a02:6b8::1]:4433", true},
		"2a02:6b8::1:4433":      {"[2a02:6b8::1]:4433", true}, // небракетированный
		"[2a02:6b8::1]":         {"", false},                  // без порта — нечего ставить в ядро
		"[2a02:6b8::1]:0":       {"", false},
		"[2a02:6b8::1]:notnum":  {"", false},
		"1.2.3.4:51820":         {"", false},
		"vpn.example.com:51820": {"", false},
	}
	for ep, want := range cases {
		got, ok := canonicalV6Endpoint(ep)
		if got != want.out || ok != want.ok {
			t.Errorf("canonicalV6Endpoint(%q) = (%q, %v), want (%q, %v)", ep, got, ok, want.out, want.ok)
		}
	}
}

// Сквозная проверка: .conf для NDMS-импорта с v6-endpoint получает заглушку,
// остальные строки не тронуты; v4-конфиг проходит байт-в-байт.
func TestReplaceConfEndpointLine_GeneratedConf(t *testing.T) {
	stored := &storage.AWGTunnel{
		Name: "t1",
		Interface: storage.AWGInterface{
			PrivateKey:     "priv",
			Address:        "10.0.0.2/32",
			AWGObfuscation: storage.AWGObfuscation{Jc: 4, Jmin: 40, Jmax: 70},
		},
		Peer: storage.AWGPeer{
			PublicKey: "pub",
			Endpoint:  "[2a02:6b8::feed:ff]:51820",
		},
	}
	conf := config.GenerateForExport(stored)
	if !strings.Contains(conf, "Endpoint = [2a02:6b8::feed:ff]:51820") {
		t.Fatalf("generated conf must carry the raw endpoint:\n%s", conf)
	}

	patched := replaceConfEndpointLine(conf, ndmsEndpointPlaceholder)
	if strings.Contains(patched, "2a02:6b8") {
		t.Fatalf("v6 endpoint must be substituted:\n%s", patched)
	}
	if !strings.Contains(patched, "Endpoint = "+ndmsEndpointPlaceholder) {
		t.Fatalf("placeholder endpoint missing:\n%s", patched)
	}
	// Всё, кроме строки Endpoint, — байт-в-байт.
	stripEndpoint := func(s string) string {
		var out []string
		for _, l := range strings.Split(s, "\n") {
			if strings.HasPrefix(strings.TrimSpace(l), "Endpoint") {
				continue
			}
			out = append(out, l)
		}
		return strings.Join(out, "\n")
	}
	if stripEndpoint(conf) != stripEndpoint(patched) {
		t.Fatal("only the Endpoint line may change")
	}
}

// Скоуп подмены — только секция [Peer]: строка с префиксом "Endpoint"
// в [Interface] (теоретически возможна внутри свободнотекстовых I-параметров)
// не трогается.
func TestReplaceConfEndpointLine_PeerSectionOnly(t *testing.T) {
	conf := "[Interface]\nPrivateKey = p\nEndpointLike = keep\nEndpoint = trap\n\n[Peer]\nPublicKey = k\nEndpoint = [2a02::1]:51820\n"
	patched := replaceConfEndpointLine(conf, ndmsEndpointPlaceholder)
	if !strings.Contains(patched, "Endpoint = trap") {
		t.Fatalf("[Interface] Endpoint-like line must be untouched:\n%s", patched)
	}
	if !strings.Contains(patched, "[Peer]\nPublicKey = k\nEndpoint = "+ndmsEndpointPlaceholder) {
		t.Fatalf("[Peer] Endpoint must be substituted:\n%s", patched)
	}
}

// Что уходит в строку Endpoint при RCI-импорте .conf. Доменное имя туда
// попадать не должно: туннель, созданный и ни разу не запущенный, поднимает
// после ребута сам NDMS, а при неудаче СВОЕГО резолва он молча не поднимает
// интерфейс — ни ошибки, ни строки в журнале (#702).
func TestImportConfEndpoint(t *testing.T) {
	stubResolveGap(t)
	cases := []struct {
		name     string
		endpoint string
		resolve  func(string) (string, int, error)
		want     string
	}{
		{
			name:     "v4-литерал — подменять нечего",
			endpoint: "1.2.3.4:51820",
			want:     "",
		},
		{
			name:     "v6-литерал — заглушка (импорт с v6 падает целиком)",
			endpoint: "[2a02::1]:51820",
			want:     ndmsEndpointPlaceholder,
		},
		{
			name:     "hostname → v4: в .conf уходит адрес",
			endpoint: "vpn.example.com:51820",
			resolve:  func(string) (string, int, error) { return "203.0.113.9", 51820, nil },
			want:     "203.0.113.9:51820",
		},
		{
			name:     "hostname → v6: заглушка, реальный endpoint выставит Start",
			endpoint: "vpn.example.com:51820",
			resolve:  func(string) (string, int, error) { return "2a02:6b8::feed:ff", 51820, nil },
			want:     ndmsEndpointPlaceholder,
		},
		{
			name:     "резолв упал — оставляем имя, прежнего адреса не существует",
			endpoint: "vpn.example.com:51820",
			resolve:  func(string) (string, int, error) { return "", 0, context.DeadlineExceeded },
			want:     "",
		},
		{
			name:     "пустой endpoint — резолвить нечего",
			endpoint: "",
			resolve: func(string) (string, int, error) {
				t.Fatal("резолвер не должен вызываться")
				return "", 0, nil
			},
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			op := newTestOperator(c.resolve)
			stored := &storage.AWGTunnel{ID: "awg1", Peer: storage.AWGPeer{Endpoint: c.endpoint}}
			if got := op.importConfEndpoint(stored); got != c.want {
				t.Fatalf("importConfEndpoint(%q) = %q, want %q", c.endpoint, got, c.want)
			}
		})
	}
}
