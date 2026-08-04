package procport

import "testing"

func TestParseListenHostPort(t *testing.T) {
	host, port, ok := ParseListenHostPort("127.0.0.1:9000", "127.0.0.1")
	if !ok || host != "127.0.0.1" || port != 9000 {
		t.Fatalf("got %q %d %v", host, port, ok)
	}
	host, port, ok = ParseListenHostPort("0.0.0.0:56002", "0.0.0.0")
	if !ok || port != 56002 {
		t.Fatalf("wan: got %q %d %v", host, port, ok)
	}
}

func TestEnrichBindErrorNoOp(t *testing.T) {
	got := EnrichBindError("", "127.0.0.1:9000", ProtoUDP)
	if got != "" {
		t.Fatalf("expected empty")
	}
}
