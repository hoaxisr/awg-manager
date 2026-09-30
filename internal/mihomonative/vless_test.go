package mihomonative

import "testing"

func TestCompileVLESSXHTTPSelectsMihomo(t *testing.T) {
	raw := "vless://11111111-2222-3333-4444-555555555555@nl.example:443?type=xhttp&security=reality&sni=cdn.example&pbk=pub&sid=abcd&fp=chrome&path=%2Fapi&host=edge.example&mode=stream-up#NL-XHTTP"
	node, err := CompileVLESS(raw, EngineAuto, EngineSingbox)
	if err != nil {
		t.Fatal(err)
	}
	if node.SelectedEngine != EngineMihomo {
		t.Fatalf("engine=%q", node.SelectedEngine)
	}
	if node.Compatibility.Singbox.Supported {
		t.Fatal("xhttp unexpectedly supported by sing-box compiler")
	}
	if node.NativeConfig["network"] != "xhttp" {
		t.Fatalf("network=%v", node.NativeConfig["network"])
	}
	opts, ok := node.NativeConfig["xhttp-opts"].(map[string]interface{})
	if !ok || opts["mode"] != "stream-up" || opts["path"] != "/api" {
		t.Fatalf("xhttp opts=%#v", opts)
	}
}

func TestCompileVLESSForcedIncompatibleEngineFails(t *testing.T) {
	_, err := CompileVLESS("vless://id@host:443?type=xhttp#x", EngineSingbox, EngineMihomo)
	if err == nil {
		t.Fatal("expected compatibility error")
	}
}

func TestCompileVLESSUsesRoutingEngineForPortableTransport(t *testing.T) {
	node, err := CompileVLESS("vless://id@host:443?type=grpc&serviceName=svc#g", EngineAuto, EngineSingbox)
	if err != nil {
		t.Fatal(err)
	}
	if node.SelectedEngine != EngineSingbox {
		t.Fatalf("engine=%q", node.SelectedEngine)
	}
}

func TestCompileVLESSXHTTPExtra(t *testing.T) {
	raw := "vless://777ed60e-8109-4008-b005-eb502ae3f0f7@cdn.vlbit.online:443?type=xhttp&mode=packet-up&path=%2Fuploadfiles%2F&host=cdn.vlbit.online&security=tls&sni=cdn.vlbit.online&alpn=h2%2Chttp%2F1.1&fp=edge&packetEncoding=xudp&extra=%7B%22uplinkHTTPMethod%22%3A%22GET%22%2C%22seqKey%22%3A%22chunk_id%22%2C%22seqPlacement%22%3A%22query%22%2C%22sessionIDKey%22%3A%22X-Upload-Token%22%2C%22sessionIDPlacement%22%3A%22header%22%2C%22xPaddingBytes%22%3A%22100-1000%22%2C%22xPaddingHeader%22%3A%22X-Client-Version%22%2C%22xPaddingKey%22%3A%22hash%22%2C%22xPaddingMethod%22%3A%22tokenish%22%2C%22xPaddingObfsMode%22%3Atrue%2C%22xPaddingPlacement%22%3A%22queryInHeader%22%7D#CDN-NEW"
	node, err := CompileVLESS(raw, EngineMihomo, EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	if node.NativeConfig["client-fingerprint"] != "edge" {
		t.Fatalf("fp=%v", node.NativeConfig["client-fingerprint"])
	}
	if node.NativeConfig["packet-encoding"] != "xudp" {
		t.Fatalf("packet-encoding=%v", node.NativeConfig["packet-encoding"])
	}
	opts, ok := node.NativeConfig["xhttp-opts"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing xhttp-opts: %#v", node.NativeConfig)
	}
	if opts["uplink-http-method"] != "GET" {
		t.Fatalf("uplink-http-method=%v", opts["uplink-http-method"])
	}
	if opts["seq-key"] != "chunk_id" || opts["seq-placement"] != "query" {
		t.Fatalf("seq-opts=%v/%v", opts["seq-key"], opts["seq-placement"])
	}
	if opts["session-key"] != "X-Upload-Token" || opts["session-placement"] != "header" {
		t.Fatalf("session-opts=%v/%v", opts["session-key"], opts["session-placement"])
	}
	if opts["x-padding-bytes"] != "100-1000" || opts["x-padding-header"] != "X-Client-Version" || opts["x-padding-obfs-mode"] != true {
		t.Fatalf("padding-opts=%#v", opts)
	}
}
