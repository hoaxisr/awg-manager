package ftlink

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// Перенос тестов link.go старого пакета (freeturn_test.go:27-72).

func TestLink_Roundtrip(t *testing.T) {
	p := LinkPayload{V: 1, Provider: "vk", Peer: "1.2.3.4:56000", Obf: "rtpopus2", Key: "aabb", MTU: 1280, WG: "[Interface]\nPrivateKey = x\n"}
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
