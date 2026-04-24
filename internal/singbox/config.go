// internal/singbox/config.go
package singbox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

const (
	firstPort        = 1080
	proxyIfacePrefix = "Proxy"
)

// Config is an in-memory mutable representation of config.json.
// We use map[string]any because sing-box config has many optional fields
// and we only manipulate inbounds/outbounds/route.rules.
type Config struct {
	raw map[string]any
}

// NewConfig creates a fresh empty config skeleton.
//
// The DNS block is explicit (rather than leaving sing-box to fall back
// on the OS resolver) for three reasons:
//
//  1. Keenetic's local resolver on 127.0.0.1:53 is flaky under load —
//     we saw i/o timeouts in production.
//  2. sing-box's default dual-stack resolution returns AAAA records that
//     router with no IPv6 egress can't route, producing
//     "network is unreachable" on outbound connects.
//  3. DoH upstream (cloudflare-dns.com) needs its hostname resolved
//     before the first query — hence the bootstrap server that speaks
//     plain UDP to an IP literal (1.1.1.1). The DoH server points its
//     domain_resolver at the bootstrap tag, breaking the chicken-and-
//     egg. The bootstrap stays on detour="direct" forever — if we
//     routed it through a tunnel, the tunnel's own hostname couldn't
//     be resolved.
//
// The DoH server has NO detour: it follows route.rules / route.final,
// so when later milestones route outbound traffic through a sing-box
// tunnel, DNS queries naturally follow and the ISP sees only ciphered
// DoH traffic to the tunnel endpoint — no DNS leak.
//
// The M1 UI will let users pick different upstreams (Google / Quad9 /
// NextDNS / custom).
func NewConfig() *Config {
	return &Config{
		raw: map[string]any{
			"log": map[string]any{"level": "info", "timestamp": true},
			"dns": map[string]any{
				"strategy": "ipv4_only",
				"servers": []any{
					// sing-box 1.13+ native schema: when `type` is set,
					// the server is addressed via `server` + optional
					// `server_port`/`path`, NOT the legacy `address`
					// field (that was the 1.11/1.12 shape). `address`
					// is rejected with "unknown field" once a type is
					// declared.
					// Bootstrap omits `detour` intentionally: sing-box
					// 1.13 flags "detour to an empty direct outbound
					// makes no sense" and FATALs at startup. With
					// route.final = "direct" the DNS query ends up at
					// the same place anyway. When M2+ routes traffic
					// through a tunnel, we'll add an explicit route
					// rule pinning bootstrap UDP/53 to direct so the
					// chicken-and-egg stays broken.
					map[string]any{
						"type":   "udp",
						"tag":    "dns-bootstrap",
						"server": "1.1.1.1",
					},
					map[string]any{
						"type":            "https",
						"tag":             "dns-doh",
						"server":          "cloudflare-dns.com",
						"domain_resolver": "dns-bootstrap",
					},
				},
				"final": "dns-doh",
			},
			"experimental": map[string]any{
				// Port 9099 (not the default 9090) so a user-managed
				// sing-box instance already bound to 9090 doesn't steal
				// our log/traffic streams — we'd otherwise forward the
				// user's tunnels into our UI and miss our own events.
				"clash_api": map[string]any{
					"external_controller": "127.0.0.1:9099",
				},
			},
			"inbounds":  []any{},
			"outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}},
			"route": map[string]any{
				"rules": []any{},
				"final": "direct",
				// sing-box 1.12+ requires a default resolver for
				// outbound `server` hostnames (naive / vless / etc.).
				// Pointing at dns-bootstrap uses plain UDP to an IP
				// literal — no chicken-and-egg when the tunnel itself
				// is what needs resolving at startup.
				"default_domain_resolver": "dns-bootstrap",
			},
		},
	}
}

// LoadConfig reads a config.json file from disk.
func LoadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse config.json: %w", err)
	}
	return &Config{raw: m}, nil
}

// Save atomically writes config.json to disk (tmp file + rename).
func (c *Config) Save(path string) error {
	b, err := json.MarshalIndent(c.raw, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp) // best-effort cleanup
		return err
	}
	return nil
}

func (c *Config) inbounds() []any {
	v, _ := c.raw["inbounds"].([]any)
	return v
}

func (c *Config) outbounds() []any {
	v, _ := c.raw["outbounds"].([]any)
	return v
}

func (c *Config) routeRules() []any {
	route, _ := c.raw["route"].(map[string]any)
	rules, _ := route["rules"].([]any)
	return rules
}

func (c *Config) setInbounds(v []any)  { c.raw["inbounds"] = v }
func (c *Config) setOutbounds(v []any) { c.raw["outbounds"] = v }
func (c *Config) setRouteRules(v []any) {
	route, _ := c.raw["route"].(map[string]any)
	if route == nil {
		route = map[string]any{"final": "direct"}
		c.raw["route"] = route
	}
	route["rules"] = v
}

// userOutbounds returns outbounds excluding system ones (direct, block).
func (c *Config) userOutbounds() []map[string]any {
	var out []map[string]any
	for _, v := range c.outbounds() {
		ob, ok := v.(map[string]any)
		if !ok {
			continue
		}
		t, _ := ob["type"].(string)
		if t == "direct" || t == "block" || t == "dns" {
			continue
		}
		out = append(out, ob)
	}
	return out
}

// Tunnels derives the UI-facing list from current config state.
func (c *Config) Tunnels() []TunnelInfo {
	userObs := c.userOutbounds()
	// Build maps tag→inbound, tag→port
	tagToPort := map[string]int{}
	for _, v := range c.inbounds() {
		ib, ok := v.(map[string]any)
		if !ok {
			continue
		}
		tag, _ := ib["tag"].(string)
		port, _ := toInt(ib["listen_port"])
		if tag != "" && port > 0 {
			// inbound tag is "<outboundTag>-in" — strip suffix
			if len(tag) > 3 && tag[len(tag)-3:] == "-in" {
				tagToPort[tag[:len(tag)-3]] = port
			}
		}
	}
	// Build list in outbound order (deterministic)
	out := make([]TunnelInfo, 0, len(userObs))
	for _, ob := range userObs {
		tag, _ := ob["tag"].(string)
		listenPort := tagToPort[tag]
		proxyIface := ""
		kernelIface := ""
		if listenPort >= firstPort {
			slot := listenPort - firstPort
			proxyIface = fmt.Sprintf("%s%d", proxyIfacePrefix, slot)
			kernelIface = fmt.Sprintf("t2s%d", slot)
		}
		info := TunnelInfo{
			Tag:             tag,
			Protocol:        strOr(ob["type"], ""),
			Server:          strOr(ob["server"], ""),
			Port:            mustInt(ob["server_port"]),
			ListenPort:      listenPort,
			ProxyInterface:  proxyIface,
			KernelInterface: kernelIface,
			Security:        detectSecurity(ob),
			Transport:       detectTransport(ob),
			SNI:             detectSNI(ob),
			Fingerprint:     detectFingerprint(ob),
			Username:        strOr(ob["username"], ""),
		}
		out = append(out, info)
	}
	return out
}

// AddTunnel inserts inbound + outbound + route rule for a new tunnel.
// Returns error if tag already exists. Picks listen_port internally via
// allocPort — use AddTunnelWithListenPort when the caller needs the
// listen_port to align with an externally-chosen ProxyN slot.
func (c *Config) AddTunnel(tag, protocol, server string, port int, outbound json.RawMessage) error {
	return c.AddTunnelWithListenPort(tag, protocol, server, port, 0, outbound)
}

// AddTunnelWithListenPort is like AddTunnel but lets the caller pin the
// listen_port. Pass 0 to fall back to allocPort (equivalent to AddTunnel).
// A non-zero listenPort is rejected if already taken in this config.
func (c *Config) AddTunnelWithListenPort(tag, protocol, server string, port, listenPort int, outbound json.RawMessage) error {
	for _, ob := range c.userOutbounds() {
		if t, _ := ob["tag"].(string); t == tag {
			return fmt.Errorf("tunnel tag %q already exists", tag)
		}
	}
	if listenPort == 0 {
		p, err := c.allocPort()
		if err != nil {
			return err
		}
		listenPort = p
	} else {
		for _, v := range c.inbounds() {
			ib, ok := v.(map[string]any)
			if !ok {
				continue
			}
			if p, ok := toInt(ib["listen_port"]); ok && p == listenPort {
				return fmt.Errorf("listen_port %d already in use", listenPort)
			}
		}
	}

	// Unmarshal outbound and force tag
	var obMap map[string]any
	if err := json.Unmarshal(outbound, &obMap); err != nil {
		return fmt.Errorf("bad outbound json: %w", err)
	}
	obMap["tag"] = tag

	// Insert inbound before existing (any order works)
	inbound := map[string]any{
		"type":        "mixed",
		"tag":         tag + "-in",
		"listen":      "127.0.0.1",
		"listen_port": listenPort,
	}
	c.setInbounds(append(c.inbounds(), inbound))

	// Insert outbound before direct
	obs := c.outbounds()
	// direct always last — insert user outbound before it
	insertAt := len(obs)
	for i, v := range obs {
		if ob, ok := v.(map[string]any); ok {
			if t, _ := ob["type"].(string); t == "direct" {
				insertAt = i
				break
			}
		}
	}
	obs = append(obs[:insertAt], append([]any{obMap}, obs[insertAt:]...)...)
	c.setOutbounds(obs)

	// Insert route rule at front (specific-before-general)
	rule := map[string]any{"inbound": tag + "-in", "outbound": tag}
	c.setRouteRules(append([]any{rule}, c.routeRules()...))

	return nil
}

// RemoveTunnel strips inbound, outbound, and route rule with matching tag.
func (c *Config) RemoveTunnel(tag string) error {
	found := false
	// outbounds
	newObs := make([]any, 0, len(c.outbounds()))
	for _, v := range c.outbounds() {
		ob, ok := v.(map[string]any)
		if !ok {
			newObs = append(newObs, v)
			continue
		}
		if t, _ := ob["tag"].(string); t == tag {
			found = true
			continue
		}
		newObs = append(newObs, v)
	}
	if !found {
		return fmt.Errorf("%w: %q", ErrTunnelNotFound, tag)
	}
	c.setOutbounds(newObs)

	// inbounds
	inTag := tag + "-in"
	newIbs := make([]any, 0, len(c.inbounds()))
	for _, v := range c.inbounds() {
		ib, ok := v.(map[string]any)
		if !ok {
			newIbs = append(newIbs, v)
			continue
		}
		if t, _ := ib["tag"].(string); t == inTag {
			continue
		}
		newIbs = append(newIbs, v)
	}
	c.setInbounds(newIbs)

	// route rules
	newRules := make([]any, 0, len(c.routeRules()))
	for _, v := range c.routeRules() {
		r, ok := v.(map[string]any)
		if !ok {
			newRules = append(newRules, v)
			continue
		}
		if ob, _ := r["outbound"].(string); ob == tag {
			continue
		}
		newRules = append(newRules, v)
	}
	c.setRouteRules(newRules)

	return nil
}

// UpdateTunnel replaces the outbound JSON for an existing tag. Inbound and route stay.
func (c *Config) UpdateTunnel(tag string, outbound json.RawMessage) error {
	var obMap map[string]any
	if err := json.Unmarshal(outbound, &obMap); err != nil {
		return fmt.Errorf("bad outbound json: %w", err)
	}
	obMap["tag"] = tag

	found := false
	obs := c.outbounds()
	for i, v := range obs {
		ob, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := ob["tag"].(string); t == tag {
			obs[i] = obMap
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%w: %q", ErrTunnelNotFound, tag)
	}
	c.setOutbounds(obs)
	return nil
}

// GetOutbound returns the raw outbound JSON for a tag.
func (c *Config) GetOutbound(tag string) (json.RawMessage, error) {
	for _, v := range c.outbounds() {
		ob, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := ob["tag"].(string); t == tag {
			b, err := json.Marshal(ob)
			return b, err
		}
	}
	return nil, fmt.Errorf("%w: %q", ErrTunnelNotFound, tag)
}

// allocPort finds the lowest free port starting from firstPort.
func (c *Config) allocPort() (int, error) {
	used := map[int]bool{}
	for _, v := range c.inbounds() {
		ib, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if p, ok := toInt(ib["listen_port"]); ok {
			used[p] = true
		}
	}
	// Find lowest free
	ports := make([]int, 0, len(used))
	for p := range used {
		ports = append(ports, p)
	}
	sort.Ints(ports)
	cand := firstPort
	for _, p := range ports {
		if cand < p {
			break
		}
		cand = p + 1
	}
	if cand > 65535 {
		return 0, fmt.Errorf("no free listen_port available (exhausted range %d-65535)", firstPort)
	}
	return cand, nil
}

// Helpers
func toInt(v any) (int, bool) {
	switch x := v.(type) {
	case float64:
		return int(x), true
	case int:
		return x, true
	case int64:
		return int(x), true
	}
	return 0, false
}
func mustInt(v any) int { n, _ := toInt(v); return n }
func strOr(v any, def string) string {
	if s, ok := v.(string); ok {
		return s
	}
	return def
}

func detectSecurity(ob map[string]any) string {
	tls, ok := ob["tls"].(map[string]any)
	if !ok {
		return "none"
	}
	if _, ok := tls["reality"].(map[string]any); ok {
		return "reality"
	}
	if enabled, _ := tls["enabled"].(bool); enabled {
		return "tls"
	}
	return "none"
}

func detectTransport(ob map[string]any) string {
	switch strOr(ob["type"], "") {
	case "hysteria2":
		return "quic"
	case "naive":
		return "https"
	}
	if tr, ok := ob["transport"].(map[string]any); ok {
		return strOr(tr["type"], "tcp")
	}
	return "tcp"
}

func detectSNI(ob map[string]any) string {
	tls, ok := ob["tls"].(map[string]any)
	if !ok {
		return ""
	}
	return strOr(tls["server_name"], "")
}

func detectFingerprint(ob map[string]any) string {
	tls, ok := ob["tls"].(map[string]any)
	if !ok {
		return ""
	}
	utls, ok := tls["utls"].(map[string]any)
	if !ok {
		return ""
	}
	return strOr(utls["fingerprint"], "")
}
