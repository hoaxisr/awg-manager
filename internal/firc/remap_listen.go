package firc

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// remapListenAddr is the address fircd must listen on when it intercepts LAN
// port 53. fircd's remap DNATs <link IP>:53 to <link IP>:<dnsProxy port>
// (destination IP is kept), so a loopback-only listener receives nothing and
// every LAN DNS query times out. fircd binds [::] dual-stack and falls back to
// 0.0.0.0 when the router has no IPv6.
const remapListenAddr = "[::]"

// ensureRemapListenAddr rewrites app.dnsProxy.host.address in firc.conf to
// [::] when port-53 remap is on and the listener is loopback-only.
func (o *Operator) ensureRemapListenAddr() error {
	path := o.ConfigPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	out, changed, err := fixRemapListenAddr(raw)
	if err != nil || !changed {
		return err
	}
	if err := atomicWriteYAMLFile(path, out, 0644); err != nil {
		return err
	}
	o.log.Warn("firc: dnsProxy listened on loopback with remap53 on, switched listen address", "address", remapListenAddr)
	return nil
}

func fixRemapListenAddr(raw []byte) ([]byte, bool, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, false, fmt.Errorf("parse firc.conf: %w", err)
	}
	if len(doc.Content) == 0 {
		return raw, false, nil
	}
	dp := yamlMapGet(yamlMapGet(doc.Content[0], "app"), "dnsProxy")
	if v := yamlMapGet(dp, "disableRemap53"); v != nil && v.Value == "true" {
		return raw, false, nil
	}
	addr := yamlMapGet(yamlMapGet(dp, "host"), "address")
	if addr == nil || !isLoopbackListen(addr.Value) {
		return raw, false, nil // absent = fircd default [::]
	}
	addr.Value = remapListenAddr
	addr.Style = yaml.DoubleQuotedStyle // bare [::] parses as a flow sequence
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, false, err
	}
	_ = enc.Close()
	return buf.Bytes(), true, nil
}

func yamlMapGet(n *yaml.Node, key string) *yaml.Node {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

func isLoopbackListen(addr string) bool {
	a := strings.Trim(strings.TrimSpace(addr), "[]")
	if strings.EqualFold(a, "localhost") {
		return true
	}
	ip := net.ParseIP(a)
	return ip != nil && ip.IsLoopback()
}
