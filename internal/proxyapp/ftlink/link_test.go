package ftlink

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// Перенос тестов link.go старого пакета (freeturn_test.go:27-72).

func TestLink_Roundtrip(t *testing.T) {
	p := LinkPayload{V: 1, Provider: "vk", Peer: "1.2.3.4:56000", Mode: "tcp", Obf: "rtpopus2", Key: "aabb", MTU: 1280, WG: "[Interface]\nPrivateKey = x\n",
		KCP: &KCP{NoDelay: 1, Interval: 20, Resend: 2, NC: 1, SndWnd: 512, RcvWnd: 512, MTU: 1200, ACKNoDelay: true}}
	link, err := EncodeLink(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(link, LinkScheme) {
		t.Fatalf("no scheme prefix: %q", link)
	}
	if strings.HasSuffix(link, "=") {
		t.Fatalf("padding must be stripped (JS-generator parity): %q", link)
	}
	got, err := DecodeLink(link)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, p) {
		t.Fatalf("roundtrip mismatch:\n got %+v\nwant %+v", got, p)
	}
}

func TestStripWGConfMTU(t *testing.T) {
	conf := "[Interface]\nPrivateKey = x\nMTU = 1376\n[Peer]\nPublicKey = y\n"
	got := StripWGConfMTU(conf)
	if strings.Contains(got, "MTU") {
		t.Fatalf("MTU line must be stripped: %q", got)
	}
	if !strings.Contains(got, "PrivateKey") {
		t.Fatalf("other lines preserved: %q", got)
	}
}

func TestDecodeLink_WithoutScheme(t *testing.T) {
	link, _ := EncodeLink(LinkPayload{V: 1, Peer: "h:1"})
	got, err := DecodeLink(strings.TrimPrefix(link, LinkScheme))
	if err != nil || got.Peer != "h:1" {
		t.Fatalf("got %+v err %v", got, err)
	}
}

func TestDecodeLink_Rejects(t *testing.T) {
	for _, bad := range []string{"", "freeturn://", "freeturn://%%%", "freeturn://aGVsbG8"} {
		if _, err := DecodeLink(bad); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
}

// Перенос names_test.go старого пакета: имя приходит строкой, не инстансом.

func TestTunnelNameFromClient(t *testing.T) {
	got := TunnelNameFromClient("Клиент")
	if got != "Клиент FT" {
		t.Fatalf("got %q", got)
	}
	got = TunnelNameFromClient("Клиент FT")
	if got != "Клиент FT" {
		t.Fatalf("duplicate suffix: got %q", got)
	}
	got = TunnelNameFromClient("")
	if got != "FreeTurn FT" {
		t.Fatalf("empty name: got %q", got)
	}
}

func TestTunnelNameFromClientTruncatesByRunes(t *testing.T) {
	long := ""
	for i := 0; i < 80; i++ {
		long += "я"
	}
	got := TunnelNameFromClient(long)
	if len([]rune(got)) != 60 {
		t.Fatalf("длина в рунах = %d, want 60", len([]rune(got)))
	}
	if !utf8.ValidString(got) {
		t.Fatalf("обрезка порвала UTF-8: %q", got)
	}
}

// Ссылка апстрима 4.0: ключи bond/timing/vk пишет его uri.Config, не мы.
func TestDecodeLink_Upstream40Fields(t *testing.T) {
	raw := `{"v":1,"peer":"p:1","mode":"tcp","bond":true,"obf":"rtpopus3","key":"k","timing":20,"vk":"https://vk.ru/call/join/X"}`
	got, err := DecodeLink(LinkScheme + base64.RawURLEncoding.EncodeToString([]byte(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Bond || got.TimingMs != 20 || got.VK != "https://vk.ru/call/join/X" {
		t.Fatalf("поля 4.0 потеряны: %+v", got)
	}

	compact := `{"url":"p:1?mode=tcp&bond=1&obf-profile=rtpopus&obf-timing=20ms"}`
	got, err = DecodeLink(LinkScheme + base64.RawURLEncoding.EncodeToString([]byte(compact)))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Bond || got.TimingMs != 20 {
		t.Fatalf("компактная форма: %+v", got)
	}
}
