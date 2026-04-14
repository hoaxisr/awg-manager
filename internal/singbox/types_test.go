package singbox

import (
	"encoding/json"
	"testing"
)

func TestTunnelInfo_JSONRoundtrip(t *testing.T) {
	in := TunnelInfo{
		Tag:            "Germany VLESS",
		Protocol:       "vless",
		Server:         "de-1.example.com",
		Port:           443,
		Security:       "reality",
		Transport:      "grpc",
		ListenPort:     1080,
		ProxyInterface: "Proxy0",
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out TunnelInfo
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Fatalf("roundtrip mismatch: got %+v want %+v", out, in)
	}
}
