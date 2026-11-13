package capture

import (
	"strings"
	"testing"
	"time"
)

func dnsResponse(t *testing.T, name string, address []byte, class uint16) []byte {
	t.Helper()
	message := []byte{0x12, 0x34, 0x81, 0x80, 0, 1, 0, 1, 0, 0, 0, 0}
	for _, label := range strings.Split(name, ".") {
		message = append(message, byte(len(label)))
		message = append(message, label...)
	}
	message = append(message, 0, 0, 1, 0, 1)
	message = append(message, 0xc0, 0x0c, 0, 1)
	message = append(message, byte(class>>8), byte(class))
	message = append(message, 0, 0, 0, 60, 0, byte(len(address)))
	message = append(message, address...)
	return message
}

func TestDNSResponseMapsAddressToName(t *testing.T) {
	message := dnsResponse(t, "belenka.eu", []byte{185, 59, 211, 40}, 1)
	got := parseDNSAnswers(message, time.Unix(1000, 0))
	if len(got) != 1 || got[0].ip.String() != "185.59.211.40" || got[0].name != "belenka.eu" {
		t.Fatalf("mappings=%v", got)
	}
}

func TestDNSResponseRejectsWrongClassAndCompressionLoop(t *testing.T) {
	if got := parseDNSAnswers(dnsResponse(t, "belenka.eu", []byte{185, 59, 211, 40}, 3), time.Now()); len(got) != 0 {
		t.Fatalf("wrong class accepted: %v", got)
	}
	message := dnsResponse(t, "belenka.eu", []byte{185, 59, 211, 40}, 1)
	message[12] = 0xc0
	message[13] = 0x0c
	if got := parseDNSAnswers(message, time.Now()); len(got) != 0 {
		t.Fatalf("compression loop accepted: %v", got)
	}
}

func TestTLSClientHelloValidation(t *testing.T) {
	hello := testClientHello(t, "belenka.eu")
	if got := tlsServerName(hello); got != "belenka.eu" {
		t.Fatalf("SNI=%q", got)
	}
	for size := 0; size < len(hello); size++ {
		if got := tlsServerName(hello[:size]); got != "" {
			t.Fatalf("truncated ClientHello returned %q at %d", got, size)
		}
	}
	if got := tlsServerName([]byte("not TLS")); got != "" {
		t.Fatalf("invalid payload returned %q", got)
	}
}
