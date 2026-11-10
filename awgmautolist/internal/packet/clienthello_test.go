package packet

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

// testClientHello builds a TLS record with one ClientHello. An empty name
// omits server_name; sniLast puts a supported_versions extension before it.
func testClientHello(name string, sniLast bool) []byte {
	body := make([]byte, 0, 512)
	body = append(body, 0x03, 0x03)
	body = append(body, bytes.Repeat([]byte{0xab}, 32)...)
	body = append(body, 0)                // session_id length
	body = append(body, 0, 2, 0x13, 0x01) // cipher suites
	body = append(body, 1, 0)             // compression methods

	supportedVersions := []byte{0x00, 0x2b, 0x00, 0x03, 0x02, 0x03, 0x04}
	extensions := make([]byte, 0, 256)
	if sniLast {
		extensions = append(extensions, supportedVersions...)
	}
	if name != "" {
		extensions = append(extensions, 0, 0)
		extensions = binary.BigEndian.AppendUint16(extensions, uint16(len(name)+5))
		extensions = binary.BigEndian.AppendUint16(extensions, uint16(len(name)+3))
		extensions = append(extensions, 0)
		extensions = binary.BigEndian.AppendUint16(extensions, uint16(len(name)))
		extensions = append(extensions, name...)
	}
	extensions = append(extensions, supportedVersions...)
	body = binary.BigEndian.AppendUint16(body, uint16(len(extensions)))
	body = append(body, extensions...)

	out := []byte{0x16, 0x03, 0x01}
	out = binary.BigEndian.AppendUint16(out, uint16(len(body)+4))
	out = append(out, 0x01, 0)
	out = binary.BigEndian.AppendUint16(out, uint16(len(body)))
	out = append(out, body...)
	return out
}

func TestClientHelloSNI(t *testing.T) {
	for _, test := range []struct {
		name    string
		sni     string
		sniLast bool
		want    string
	}{
		{"basic", "example.com", false, "example.com"},
		{"after another extension", "deep.sub.example.org", true, "deep.sub.example.org"},
		{"case folding", "Deep.Sub.Example.ORG", false, "deep.sub.example.org"},
		{"root dot", "example.com.", false, "example.com"},
		{"underscore", "_acme-challenge.example.com", false, "_acme-challenge.example.com"},
		{"one label", "localhost", false, "localhost"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := ClientHelloSNI(testClientHello(test.sni, test.sniLast))
			if !ok || got != test.want {
				t.Fatalf("got %q, %v; want %q", got, ok, test.want)
			}
			alias, aliasOK := TLSClientHelloSNI(testClientHello(test.sni, test.sniLast))
			if aliasOK != ok || alias != got {
				t.Errorf("alias returned %q, %v", alias, aliasOK)
			}
		})
	}
}

func TestClientHelloWithoutSNI(t *testing.T) {
	if got, ok := ClientHelloSNI(testClientHello("", false)); ok || got != "" {
		t.Fatalf("got %q, %v for a hello without SNI", got, ok)
	}
}

func TestClientHelloRejectsOtherTLSMessages(t *testing.T) {
	raw := testClientHello("example.com", false)
	raw[0] = 0x17
	if _, ok := ClientHelloSNI(raw); ok {
		t.Error("accepted application data")
	}
	raw = testClientHello("example.com", false)
	raw[5] = 0x02
	if _, ok := ClientHelloSNI(raw); ok {
		t.Error("accepted ServerHello")
	}
}

func TestClientHelloShortCaptureOuterLengths(t *testing.T) {
	raw := testClientHello("example.com", true)
	raw[3], raw[4] = 0xff, 0xff
	got, ok := ClientHelloSNI(raw)
	if !ok || got != "example.com" {
		t.Errorf("record length beyond capture: %q, %v", got, ok)
	}
	raw = testClientHello("example.com", true)
	raw[6], raw[7], raw[8] = 0xff, 0xff, 0xff
	got, ok = ClientHelloSNI(raw)
	if !ok || got != "example.com" {
		t.Errorf("handshake length beyond capture: %q, %v", got, ok)
	}
}

func TestClientHelloEveryTruncationIsSafe(t *testing.T) {
	raw := testClientHello("example.com", true)
	for cut := 0; cut <= len(raw); cut++ {
		name, ok := ClientHelloSNI(raw[:cut])
		if ok && name != "example.com" {
			t.Fatalf("cut %d returned a different name %q", cut, name)
		}
	}
}

func TestClientHelloTruncatedNameIsNotReturned(t *testing.T) {
	raw := testClientHello("example.com", true)
	nameAt := bytes.Index(raw, []byte("example.com"))
	if nameAt < 0 {
		t.Fatal("test hello has no name")
	}
	for cut := nameAt + 1; cut < nameAt+len("example.com"); cut++ {
		if got, ok := ClientHelloSNI(raw[:cut]); ok {
			t.Errorf("cut %d returned partial name %q", cut, got)
		}
	}
	if got, ok := ClientHelloSNI(raw[:nameAt+len("example.com")]); !ok || got != "example.com" {
		t.Errorf("complete name at end of capture: %q, %v", got, ok)
	}
}

func TestClientHelloRejectsNamePastExtension(t *testing.T) {
	raw := testClientHello("example.com", false)
	nameAt := bytes.Index(raw, []byte("example.com"))
	if nameAt < 2 {
		t.Fatal("test hello has no name length")
	}
	binary.BigEndian.PutUint16(raw[nameAt-2:nameAt], 200)
	if got, ok := ClientHelloSNI(raw); ok {
		t.Errorf("accepted out-of-extension name %q", got)
	}
}

func TestClientHelloNestedLengthsCannotEscape(t *testing.T) {
	raw := testClientHello("example.com", false)
	nameAt := bytes.Index(raw, []byte("example.com"))
	if nameAt < 5 {
		t.Fatal("test hello has no list length")
	}
	// The server_name_list length includes its two own bytes in this helper's
	// extension layout only outside the list. Set the inner list to one byte so
	// no name can be read beyond it.
	binary.BigEndian.PutUint16(raw[nameAt-5:nameAt-3], 1)
	if got, ok := ClientHelloSNI(raw); ok {
		t.Errorf("accepted name beyond server_name_list: %q", got)
	}
}

func TestClientHelloSkipsNonHostNameEntries(t *testing.T) {
	// Start with an ordinary SNI extension and insert an unknown name type in
	// front of host_name, adjusting both list and extension lengths.
	raw := testClientHello("example.com", false)
	nameAt := bytes.Index(raw, []byte("example.com"))
	entryAt := nameAt - 3 // host_name type and uint16 length
	entry := append([]byte(nil), raw[entryAt:nameAt+11]...)
	unknown := []byte{2, 0, 10}
	unknown = append(unknown, "other.test"...)
	mutated := append([]byte(nil), raw[:entryAt]...)
	mutated = append(mutated, unknown...)
	mutated = append(mutated, entry...)
	mutated = append(mutated, raw[nameAt+11:]...)
	// Locate the SNI extension by its type immediately before the list.
	extAt := entryAt - 6
	binary.BigEndian.PutUint16(mutated[extAt+2:extAt+4], uint16(16+len(unknown)))
	binary.BigEndian.PutUint16(mutated[extAt+4:extAt+6], uint16(14+len(unknown)))
	// The extension vector, handshake and record all grew by the inserted entry.
	growth := uint16(len(unknown))
	binary.BigEndian.PutUint16(mutated[extAt-2:extAt], binary.BigEndian.Uint16(mutated[extAt-2:extAt])+growth)
	binary.BigEndian.PutUint16(mutated[3:5], binary.BigEndian.Uint16(mutated[3:5])+growth)
	handshakeLen := uint32(mutated[6])<<16 | uint32(mutated[7])<<8 | uint32(mutated[8])
	handshakeLen += uint32(growth)
	mutated[6], mutated[7], mutated[8] = byte(handshakeLen>>16), byte(handshakeLen>>8), byte(handshakeLen)
	got, ok := ClientHelloSNI(mutated)
	if !ok || got != "example.com" {
		t.Fatalf("non-host entry was not skipped: %q, %v", got, ok)
	}
}

func TestClientHelloStopsAtFirstServerNameExtension(t *testing.T) {
	raw := testClientHello("example.com", false)
	nameAt := bytes.Index(raw, []byte("example.com"))
	// An unknown name_type only, with no host_name following it.
	mutated := append([]byte(nil), raw...)
	mutated[nameAt-3] = 2
	if got, ok := ClientHelloSNI(mutated); ok {
		t.Errorf("accepted a server_name extension without host_name: %q", got)
	}
}

func TestClientHelloRejectsInvalidHostNames(t *testing.T) {
	longLabel := strings.Repeat("a", 64) + ".com"
	tooLong := strings.Repeat("a", 60) + "." + strings.Repeat("b", 60) + "." +
		strings.Repeat("c", 60) + "." + strings.Repeat("d", 72)
	for _, name := range []string{
		"", ".", ".example.com", "example.com..", "ex..ample.com",
		"ex\x01mple.com", "ex\x00mple.com", "bad name.example", "bad/name.example",
		longLabel, tooLong,
	} {
		raw := testClientHello(name, false)
		if name == "" { // the helper omits SNI for this case, which is also no name
			if _, ok := ClientHelloSNI(raw); ok {
				t.Error("empty SNI accepted")
			}
			continue
		}
		if got, ok := ClientHelloSNI(raw); ok {
			t.Errorf("invalid host %q accepted as %q", name, got)
		}
	}
}

func TestClientHelloHostNameLimits(t *testing.T) {
	label63 := strings.Repeat("a", 63) + ".example.com"
	if got, ok := ClientHelloSNI(testClientHello(label63, false)); !ok || got != label63 {
		t.Errorf("63-byte label rejected: %q, %v", got, ok)
	}
	// 63.63.63.61 plus dots = 253 bytes.
	name253 := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." +
		strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
	if len(name253) != 253 {
		t.Fatalf("bad test name length %d", len(name253))
	}
	if got, ok := ClientHelloSNI(testClientHello(name253, false)); !ok || got != name253 {
		t.Errorf("253-byte name rejected: len=%d, %v", len(got), ok)
	}
}

func FuzzClientHelloSNI(f *testing.F) {
	f.Add([]byte{})
	f.Add(testClientHello("example.com", false))
	f.Add(testClientHello("Deep.Sub.Example.Org.", true))
	f.Fuzz(func(t *testing.T, raw []byte) {
		_, _ = ClientHelloSNI(raw)
	})
}
