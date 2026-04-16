// internal/singbox/integration_test.go
package singbox

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIntegration_ParseAddValidate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	// Parse 3 different links
	links := []string{
		"vless://uuid-1@de.tld:443?security=reality&type=grpc&pbk=pbk1&sni=google.com&fp=chrome#Germany",
		"hy2://pw@fi.tld:8443?sni=fi.tld#Finland",
		"naive+https://u:p@jp.tld:443#Japan",
	}
	cfg := NewConfig()
	for _, link := range links {
		p, err := Parse(link)
		if err != nil {
			t.Fatalf("parse %s: %v", link, err)
		}
		if err := cfg.AddTunnel(p.Tag, p.Protocol, p.Server, p.Port, p.Outbound); err != nil {
			t.Fatal(err)
		}
	}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}

	tunnels := cfg.Tunnels()
	if len(tunnels) != 3 {
		t.Fatalf("tunnel count: %d", len(tunnels))
	}

	// Ports should be 1080, 1081, 1082
	ports := []int{tunnels[0].ListenPort, tunnels[1].ListenPort, tunnels[2].ListenPort}
	for i, want := range []int{1080, 1081, 1082} {
		if ports[i] != want {
			t.Errorf("port[%d]=%d want %d", i, ports[i], want)
		}
	}

	// Remove middle, then add another — port 1081 should be reused
	if err := cfg.RemoveTunnel("Finland"); err != nil {
		t.Fatal(err)
	}
	p, _ := Parse("vless://u@nl.tld:443#Netherlands")
	cfg.AddTunnel(p.Tag, p.Protocol, p.Server, p.Port, p.Outbound)
	var nl TunnelInfo
	for _, ti := range cfg.Tunnels() {
		if ti.Tag == "Netherlands" {
			nl = ti
		}
	}
	if nl.ListenPort != 1081 {
		t.Errorf("port reuse: got %d, want 1081", nl.ListenPort)
	}
	// ProxyInterface must be derived from listen_port, not iteration index
	if nl.ProxyInterface != "Proxy1" {
		t.Errorf("Netherlands ProxyInterface=%q, want Proxy1 (port 1081 = slot 1)", nl.ProxyInterface)
	}
	var japan TunnelInfo
	for _, ti := range cfg.Tunnels() {
		if ti.Tag == "Japan" {
			japan = ti
		}
	}
	if japan.ProxyInterface != "Proxy2" {
		t.Errorf("Japan ProxyInterface=%q, want Proxy2 (must remain stable after Finland removal)", japan.ProxyInterface)
	}

	// Validate with mock exec (real sing-box not available in CI)
	v := &Validator{
		binary: "sing-box",
		exec: func(bin string, args ...string) ([]byte, error) {
			// Check that the last arg is the absolute path to our config
			if len(args) != 3 {
				t.Errorf("args len: %v", args)
			}
			if args[2] != path {
				t.Errorf("path arg: %s, want %s", args[2], path)
			}
			_, err := os.Stat(path)
			return nil, err
		},
	}
	if err := v.Validate(path); err != nil {
		t.Fatal(err)
	}
}
