package traffic

import (
	"testing"
)

func TestExtractSNI(t *testing.T) {
	// Sample TLS ClientHello containing "example.com"
	packet := []byte{
		0x16, 0x03, 0x01, 0x00, 0x40, // Record header (TLS 1.0, length 64)
		0x01, 0x00, 0x00, 0x3c, // Handshake type 1 (ClientHello), length 60
		0x03, 0x03, // TLS 1.2
	}
	// 32 random bytes
	packet = append(packet, make([]byte, 32)...)
	// Session ID len 0
	packet = append(packet, 0x00)
	// Cipher suites len 2 (one suite 0x1301)
	packet = append(packet, 0x00, 0x02, 0x13, 0x01)
	// Compression methods len 1 (0x00)
	packet = append(packet, 0x01, 0x00)

	// Extensions: SNI extension
	sniExt := []byte{
		0x00, 0x00, // ext type 0 (server_name)
		0x00, 0x10, // ext len 16
		0x00, 0x0e, // server name list len 14
		0x00,       // name type 0 (host_name)
		0x00, 0x0b, // name len 11
		'e', 'x', 'a', 'm', 'p', 'l', 'e', '.', 'c', 'o', 'm',
	}

	packet = append(packet, 0x00, byte(len(sniExt))) // extensions len
	packet = append(packet, sniExt...)

	// Update length fields
	packet[3] = byte(len(packet) - 5 >> 8)
	packet[4] = byte(len(packet) - 5)
	packet[7] = byte(len(packet) - 9)

	sni := ExtractSNI(packet)
	if sni != "example.com" {
		t.Fatalf("expected example.com, got %q", sni)
	}
}

func TestParseDNSAnswers(t *testing.T) {
	// Sample DNS Response: query "google.com" -> A 142.250.74.206
	dns := []byte{
		0x12, 0x34, // ID
		0x81, 0x80, // Flags (QR=1, RD=1, RA=1)
		0x00, 0x01, // QDCOUNT = 1
		0x00, 0x01, // ANCOUNT = 1
		0x00, 0x00, // NSCOUNT = 0
		0x00, 0x00, // ARCOUNT = 0
		// Question: \x06google\x03com\x00, Type 1 (A), Class 1 (IN)
		0x06, 'g', 'o', 'o', 'g', 'l', 'e', 0x03, 'c', 'o', 'm', 0x00,
		0x00, 0x01, 0x00, 0x01,
		// Answer: pointer 0xc00c, Type 1, Class 1, TTL 300 (0x0000012c), RDLength 4, IP 142.250.74.206
		0xc0, 0x0c,
		0x00, 0x01, 0x00, 0x01,
		0x00, 0x00, 0x01, 0x2c,
		0x00, 0x04,
		142, 250, 74, 206,
	}

	m := ParseDNSAnswers(dns)
	if m["142.250.74.206"] != "google.com" {
		t.Fatalf("expected google.com, got %v", m)
	}
}
