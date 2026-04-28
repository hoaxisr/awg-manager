// internal/singbox/config.go
package singbox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
			"outbounds": []any{},
			"route": map[string]any{
				"rules": []any{},
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

// userOutbounds returns outbounds excluding system ones (direct, block, dns,
// selector) and device-proxy AWG infrastructure outbounds.
// Device-proxy AWG outbounds have type=direct with bind_interface set; they
// are already excluded by the t=="direct" clause below. A tag-prefix filter
// (awg-*) is deliberately not used: it would silently drop any legitimate
// user tunnel whose tag happens to start with "awg-", which is a legal name
// with no validation guard.
func (c *Config) userOutbounds() []map[string]any {
	var out []map[string]any
	for _, v := range c.outbounds() {
		ob, ok := v.(map[string]any)
		if !ok {
			continue
		}
		t, _ := ob["type"].(string)
		if t == "direct" || t == "block" || t == "dns" || t == "selector" {
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

	c.setOutbounds(append(c.outbounds(), obMap))

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

// DeviceProxySpec is the externally-supplied description of the
// user-facing proxy. Each EnsureDeviceProxy call recomputes the
// inbound + selector outbound from this spec. AWG-direct outbounds
// are NOT created here — they live in 15-awg.json owned by the
// awgoutbounds package. Spec only references their tags.
type DeviceProxySpec struct {
	Enabled     bool
	ListenAddr  string   // already resolved to an IP literal
	Port        int
	Auth        DeviceProxyAuth
	SelectedTag string   // member tag that becomes selector.default
	AWGTags     []string // canonical tags from awgoutbounds (e.g. "awg-foo", "awg-sys-Wireguard0")
	SBTags      []string // sing-box tunnel tags (user outbounds)
}

type DeviceProxyAuth struct {
	Enabled  bool
	Username string
	Password string
}

const (
	deviceProxyInboundTag  = "device-proxy-in"
	deviceProxySelectorTag = "device-proxy-selector"
	deviceProxyAWGPrefix   = "awg-"
)

// EnsureDeviceProxy writes (or overwrites) the inbound + selector
// outbound + route rule described by spec. Idempotent. Callers that
// toggle Enabled=false should use RemoveDeviceProxy.
//
// AWG-direct outbounds are NOT created or touched by this function —
// awgoutbounds owns 15-awg.json and tags from spec.AWGTags must
// resolve to outbounds already declared there. Legacy awg-* outbounds
// from pre-refactor builds are stripped on every call (idempotent).
func (c *Config) EnsureDeviceProxy(spec DeviceProxySpec) error {
	if !spec.Enabled {
		c.RemoveDeviceProxy()
		return nil
	}

	// Inbound
	inbound := map[string]any{
		"type":        "mixed",
		"tag":         deviceProxyInboundTag,
		"listen":      spec.ListenAddr,
		"listen_port": spec.Port,
	}
	if spec.Auth.Enabled {
		inbound["users"] = []any{
			map[string]any{
				"username": spec.Auth.Username,
				"password": spec.Auth.Password,
			},
		}
	}
	c.upsertInbound(deviceProxyInboundTag, inbound)

	// Strip any legacy awg-* outbounds left over from the pre-15-awg.json
	// era (one-shot cleanup; idempotent, no-op once the file is clean).
	c.pruneAWGOutbounds(nil)

	// Selector outbound — members in deterministic order: direct, sb tags,
	// sorted awg tags from spec. AWG outbounds themselves live in
	// 15-awg.json; the selector just references them by tag.
	members := []any{"direct"}
	for _, tag := range spec.SBTags {
		members = append(members, tag)
	}
	awgTagsCopy := append([]string(nil), spec.AWGTags...)
	sort.Strings(awgTagsCopy)
	for _, tag := range awgTagsCopy {
		members = append(members, tag)
	}
	selector := map[string]any{
		"type":      "selector",
		"tag":       deviceProxySelectorTag,
		"outbounds": members,
	}
	if spec.SelectedTag != "" {
		selector["default"] = spec.SelectedTag
	}
	c.upsertOutbound(deviceProxySelectorTag, selector)

	// Route rule at front of rules list.
	c.ensureDeviceProxyRouteRule()
	return nil
}

// RemoveDeviceProxy strips every artefact EnsureDeviceProxy adds.
// Idempotent — safe on a config that never had the proxy.
func (c *Config) RemoveDeviceProxy() {
	c.removeInbound(deviceProxyInboundTag)
	c.removeOutbound(deviceProxySelectorTag)
	c.pruneAWGOutbounds(nil)
	c.removeDeviceProxyRouteRule()
}

// upsertInbound replaces the inbound whose tag matches, or appends if
// none matches. Preserves the order of existing inbounds.
func (c *Config) upsertInbound(tag string, inbound map[string]any) {
	inbounds := c.inbounds()
	for i, v := range inbounds {
		ib, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := ib["tag"].(string); t == tag {
			inbounds[i] = inbound
			c.setInbounds(inbounds)
			return
		}
	}
	c.setInbounds(append(inbounds, inbound))
}

func (c *Config) removeInbound(tag string) {
	inbounds := c.inbounds()
	out := make([]any, 0, len(inbounds))
	for _, v := range inbounds {
		ib, ok := v.(map[string]any)
		if !ok {
			out = append(out, v)
			continue
		}
		if t, _ := ib["tag"].(string); t == tag {
			continue
		}
		out = append(out, v)
	}
	c.setInbounds(out)
}

// upsertOutbound replaces or appends by tag. Inserts before the trailing
// `direct` outbound so the route.final direct-fallback stays at the end.
func (c *Config) upsertOutbound(tag string, outbound map[string]any) {
	obs := c.outbounds()
	for i, v := range obs {
		ob, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := ob["tag"].(string); t == tag {
			obs[i] = outbound
			c.setOutbounds(obs)
			return
		}
	}
	insertAt := len(obs)
	for i, v := range obs {
		ob, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := ob["tag"].(string); t == "direct" {
			insertAt = i
			break
		}
	}
	obs = append(obs[:insertAt], append([]any{outbound}, obs[insertAt:]...)...)
	c.setOutbounds(obs)
}

func (c *Config) removeOutbound(tag string) {
	obs := c.outbounds()
	out := make([]any, 0, len(obs))
	for _, v := range obs {
		ob, ok := v.(map[string]any)
		if !ok {
			out = append(out, v)
			continue
		}
		if t, _ := ob["tag"].(string); t == tag {
			continue
		}
		out = append(out, v)
	}
	c.setOutbounds(out)
}

// pruneAWGOutbounds removes every `awg-*` outbound whose tag is not in
// keep. Pass nil to remove all.
func (c *Config) pruneAWGOutbounds(keep map[string]string) {
	obs := c.outbounds()
	out := make([]any, 0, len(obs))
	for _, v := range obs {
		ob, ok := v.(map[string]any)
		if !ok {
			out = append(out, v)
			continue
		}
		tag, _ := ob["tag"].(string)
		if strings.HasPrefix(tag, deviceProxyAWGPrefix) {
			if _, stay := keep[tag]; !stay {
				continue
			}
		}
		out = append(out, v)
	}
	c.setOutbounds(out)
}

func (c *Config) ensureDeviceProxyRouteRule() {
	rule := map[string]any{
		"inbound":  deviceProxyInboundTag,
		"outbound": deviceProxySelectorTag,
	}
	rules := c.routeRules()
	filtered := make([]any, 0, len(rules))
	for _, v := range rules {
		r, ok := v.(map[string]any)
		if !ok {
			filtered = append(filtered, v)
			continue
		}
		if r["inbound"] == deviceProxyInboundTag {
			continue
		}
		filtered = append(filtered, v)
	}
	c.setRouteRules(append([]any{rule}, filtered...))
}

func (c *Config) removeDeviceProxyRouteRule() {
	rules := c.routeRules()
	out := make([]any, 0, len(rules))
	for _, v := range rules {
		r, ok := v.(map[string]any)
		if !ok {
			out = append(out, v)
			continue
		}
		if r["inbound"] == deviceProxyInboundTag {
			continue
		}
		out = append(out, v)
	}
	c.setRouteRules(out)
}

// MigrateLegacyConfigDir splits an old monolithic config.json into the
// new config.d/ layout (00-base.json + 10-tunnels.json) on first run.
// No-op when config.d already exists. Used by Operator.New to handle
// upgrades from pre-router-engine builds.
func MigrateLegacyConfigDir(dir string) error {
	configDir := filepath.Join(dir, "config.d")
	if _, err := os.Stat(configDir); err == nil {
		return nil
	}

	legacyPath := filepath.Join(dir, "config.json")
	raw, err := os.ReadFile(legacyPath)
	if err != nil {
		if os.IsNotExist(err) {
			return os.MkdirAll(configDir, 0755)
		}
		return err
	}

	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("parse legacy config: %w", err)
	}

	if err := os.MkdirAll(configDir, 0755); err != nil {
		return err
	}

	base := map[string]json.RawMessage{}
	for _, k := range []string{"log", "experimental", "dns"} {
		if v, ok := cfg[k]; ok {
			base[k] = v
		}
	}
	if err := writeJSONFile(filepath.Join(configDir, "00-base.json"), base); err != nil {
		return err
	}

	tunnels := map[string]json.RawMessage{}
	for _, k := range []string{"inbounds", "outbounds", "route"} {
		if v, ok := cfg[k]; ok {
			tunnels[k] = v
		}
	}
	if err := writeJSONFile(filepath.Join(configDir, "10-tunnels.json"), tunnels); err != nil {
		return err
	}

	return os.Remove(legacyPath)
}

// writeJSONFile is the shared atomic-ish JSON writer used by
// MigrateLegacyConfigDir + ensureBaseConfig. Marshals with indent for
// human-editable fragments.
func writeJSONFile(path string, data any) error {
	raw, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0644)
}
