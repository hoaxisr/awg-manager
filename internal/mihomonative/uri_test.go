package mihomonative

import "testing"

const trustTunnelConnectTestURL = "https://trustunnel.ru/connect/?d=ARF1czMudHJ1dHVuLm9ubGluZQUPdXNlcl8xMzUzODE4OTc5Bgw4ZU9wclZwYXhReDYCFXVzMy50cnV0dW4ub25saW5lOjQ0MwsIZjcwMDQ4YWYDEXVzMy50cnV0dW4ub25saW5lDB_wn4e68J-HuCBVU0EgKNCh0KjQkCkgKFByZW1pdW0pDUBBJWh0dHBzOi8vZG5zLmFkZ3VhcmQtZG5zLmNvbS9kbnMtcXVlcnkacXVpYzovL2Rucy5hZGd1YXJkLWRucy5jb20EAQAKAQEJAQI"

func TestCompileTrustTunnelConnectURL(t *testing.T) {
	nodes, err := CompileURI(trustTunnelConnectTestURL, EngineMihomo, EngineSingbox)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 endpoint, got %d", len(nodes))
	}
	node := nodes[0]
	if node.Protocol != "trusttunnel" || node.SelectedEngine != EngineMihomo {
		t.Fatalf("node: %#v", node)
	}
	if node.NativeConfig["type"] != "trusttunnel" || node.NativeConfig["port"] != 443 {
		t.Fatalf("config: %#v", node.NativeConfig)
	}
	if node.NativeConfig["username"] != "user_1353818979" || node.NativeConfig["password"] != "8eOprVpaxQx6" {
		t.Fatalf("auth: %#v", node.NativeConfig)
	}
	if node.NativeConfig["sni"] != "us3.trutun.online" || node.NativeConfig["quic"] != true || node.Transport != "quic" {
		t.Fatalf("transport: %#v (transport=%s)", node.NativeConfig, node.Transport)
	}
}

func TestCompileTrustTunnelCannotSelectSingboxFromNativeStore(t *testing.T) {
	_, err := CompileURI(trustTunnelConnectTestURL, EngineSingbox, EngineMihomo)
	if err == nil {
		t.Fatal("expected incompatible engine error")
	}
}

func TestCompileHysteria2URI(t *testing.T) {
	raw := "hysteria2://secret@hy.example:443?mport=20000-30000&hop_interval=15s&obfs=salamander&obfs-password=mask&sni=edge.example&insecure=1#HY2"
	nodes, err := CompileURI(raw, EngineMihomo, EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	proxy := nodes[0].NativeConfig
	if proxy["type"] != "hysteria2" || proxy["password"] != "secret" {
		t.Fatalf("config: %#v", proxy)
	}
	if proxy["ports"] != "20000-30000" || proxy["hop-interval"] != 15 {
		t.Fatalf("port hopping: %#v", proxy)
	}
	if proxy["obfs"] != "salamander" || proxy["obfs-password"] != "mask" {
		t.Fatalf("obfs: %#v", proxy)
	}
	if proxy["sni"] != "edge.example" || proxy["skip-cert-verify"] != true {
		t.Fatalf("tls: %#v", proxy)
	}
}

func TestCompileTrojanWebSocketURI(t *testing.T) {
	raw := "trojan://secret@tr.example:443?type=ws&path=%2Fsocket&host=cdn.example&sni=edge.example#Trojan"
	nodes, err := CompileURI(raw, EngineMihomo, EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	proxy := nodes[0].NativeConfig
	if proxy["type"] != "trojan" || proxy["password"] != "secret" || proxy["network"] != "ws" {
		t.Fatalf("config: %#v", proxy)
	}
	if proxy["sni"] != "edge.example" {
		t.Fatalf("tls: %#v", proxy)
	}
	opts, ok := proxy["ws-opts"].(map[string]interface{})
	if !ok || opts["path"] != "/socket" {
		t.Fatalf("ws opts: %#v", proxy["ws-opts"])
	}
}

func TestCompileHysteriaURI(t *testing.T) {
	raw := "hysteria://hy.example:443?auth=secret&protocol=faketcp&upmbps=30&downmbps=200&peer=edge.example&insecure=1#HY1"
	nodes, err := CompileURI(raw, EngineMihomo, EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	proxy := nodes[0].NativeConfig
	if proxy["type"] != "hysteria" || proxy["auth-str"] != "secret" || proxy["protocol"] != "faketcp" {
		t.Fatalf("config: %#v", proxy)
	}
	if proxy["up"] != "30" || proxy["down"] != "200" || proxy["sni"] != "edge.example" {
		t.Fatalf("options: %#v", proxy)
	}
}

func TestCompileTUICV5URI(t *testing.T) {
	raw := "tuic://00000000-0000-0000-0000-000000000001:secret@tuic.example:10443?udp_relay_mode=native&congestion_control=bbr&sni=edge.example&allowInsecure=1#TUIC"
	nodes, err := CompileURI(raw, EngineMihomo, EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	proxy := nodes[0].NativeConfig
	if proxy["uuid"] != "00000000-0000-0000-0000-000000000001" || proxy["password"] != "secret" {
		t.Fatalf("auth: %#v", proxy)
	}
	if proxy["udp-relay-mode"] != "native" || proxy["congestion-controller"] != "bbr" {
		t.Fatalf("options: %#v", proxy)
	}
}

func TestCompileAnyTLSURIAndRejectReality(t *testing.T) {
	nodes, err := CompileURI("anytls://secret@any.example:443?sni=edge.example&fp=chrome#AnyTLS", EngineMihomo, EngineMihomo)
	if err != nil {
		t.Fatal(err)
	}
	proxy := nodes[0].NativeConfig
	if proxy["password"] != "secret" || proxy["udp"] != true || proxy["client-fingerprint"] != "chrome" {
		t.Fatalf("config: %#v", proxy)
	}
	if _, err := CompileURI("anytls://secret@any.example:443?security=reality&pbk=x", EngineMihomo, EngineMihomo); err == nil {
		t.Fatal("expected AnyTLS Reality rejection")
	}
}
