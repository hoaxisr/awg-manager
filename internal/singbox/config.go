// internal/singbox/config.go
package singbox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
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

// NewConfig returns the empty slot-shape skeleton for 10-tunnels.json:
// inbounds + outbounds + route only. log/dns/experimental are intentionally
// omitted — those keys belong to 00-base.json (owned by ensureBaseConfig).
// Emitting them here used to pollute 10-tunnels.json on first tunnel-add
// with dns-bootstrap/dns-doh tags that 00-base owns, tripping the
// orchestrator's cross-slot duplicate-tag validator.
func NewConfig() *Config {
	return &Config{
		raw: map[string]any{
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

// MarshalJSON exposes the in-memory config as the same JSON shape Save
// would write. Lets callers (orchestrator slot builders, migrations,
// tests) obtain bytes without going through disk.
func (c *Config) MarshalJSON() ([]byte, error) {
	return json.Marshal(c.raw)
}

// UnmarshalJSON parses a sing-box config document into c. Mirrors
// LoadConfig but for callers that already hold the bytes.
func (c *Config) UnmarshalJSON(data []byte) error {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	c.raw = m
	return nil
}

// Save atomically writes config.json to disk (tmp file + rename).
func (c *Config) Save(path string) error {
	c.ensureNaiveUDPOverTCPOutbounds()
	b, err := json.MarshalIndent(c.raw, "", "  ")
	if err != nil {
		return err
	}
	return storage.AtomicWrite(path, b)
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
	ensureNaiveUDPOverTCP(obMap)

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
	ensureNaiveUDPOverTCP(obMap)

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

// RenameTunnel changes a user tunnel tag and all local references owned by
// 10-tunnels.json. It preserves the listen_port, therefore the ProxyN/t2sN
// slot remains stable.
func (c *Config) RenameTunnel(oldTag, newTag string) error {
	oldTag = strings.TrimSpace(oldTag)
	newTag = strings.TrimSpace(newTag)
	if oldTag == "" || newTag == "" {
		return ErrInvalidTunnelTag
	}
	if oldTag == newTag {
		found := false
		for _, ob := range c.userOutbounds() {
			if t, _ := ob["tag"].(string); t == oldTag {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%w: %q", ErrTunnelNotFound, oldTag)
		}
		return nil
	}

	found := false
	for _, ob := range c.userOutbounds() {
		t, _ := ob["tag"].(string)
		if t == oldTag {
			found = true
		}
	}
	for _, v := range c.outbounds() {
		ob, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := ob["tag"].(string); t == newTag {
			return fmt.Errorf("%w: %q", ErrTunnelTagConflict, newTag)
		}
	}
	if !found {
		return fmt.Errorf("%w: %q", ErrTunnelNotFound, oldTag)
	}

	for _, v := range c.outbounds() {
		ob, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := ob["tag"].(string); t == oldTag {
			ob["tag"] = newTag
		}
	}

	oldInTag := oldTag + "-in"
	newInTag := newTag + "-in"
	for _, v := range c.inbounds() {
		ib, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := ib["tag"].(string); t == oldInTag {
			ib["tag"] = newInTag
		}
	}

	renameTunnelRouteRefs(c.routeRules(), oldTag, newTag, oldInTag, newInTag)
	return nil
}

func ensureNaiveUDPOverTCP(ob map[string]any) bool {
	if strOr(ob["type"], "") != "naive" {
		return false
	}
	if _, ok := ob["udp_over_tcp"]; ok {
		return false
	}
	ob["udp_over_tcp"] = map[string]any{
		"enabled": true,
		"version": 2,
	}
	return true
}

func (c *Config) ensureNaiveUDPOverTCPOutbounds() bool {
	changed := false
	for _, raw := range c.outbounds() {
		ob, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if ensureNaiveUDPOverTCP(ob) {
			changed = true
		}
	}
	return changed
}

func renameTunnelRouteRefs(values []any, oldOut, newOut, oldIn, newIn string) {
	for _, v := range values {
		r, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if ob, _ := r["outbound"].(string); ob == oldOut {
			r["outbound"] = newOut
		}
		switch in := r["inbound"].(type) {
		case string:
			if in == oldIn {
				r["inbound"] = newIn
			}
		case []any:
			for i, raw := range in {
				if s, _ := raw.(string); s == oldIn {
					in[i] = newIn
				}
			}
		}
		if nested, ok := r["rules"].([]any); ok {
			renameTunnelRouteRefs(nested, oldOut, newOut, oldIn, newIn)
		}
	}
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
	case "mieru":
		return strings.ToLower(strOr(ob["transport"], "tcp"))
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
	ListenAddr  string // already resolved to an IP literal
	Port        int
	Auth        DeviceProxyAuth
	SelectedTag string   // member tag that becomes selector.default
	AWGTags     []string // canonical tags from awgoutbounds (e.g. "awg-foo", "awg-sys-Wireguard0")
	SBTags      []string // sing-box tunnel tags (user outbounds)
}

// DeviceProxyInstanceSpec describes one user-facing proxy instance.
// ID controls generated inbound/selector tags. ID "default" keeps the
// legacy tags device-proxy-in / device-proxy-selector for compatibility.
type DeviceProxyInstanceSpec struct {
	ID          string
	Enabled     bool
	ListenAddr  string
	Port        int
	Auth        DeviceProxyAuth
	SelectedTag string
	AWGTags     []string
	SBTags      []string
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

func deviceProxyInstanceTags(id string) (inboundTag, selectorTag string) {
	if id == "" || id == "default" {
		return deviceProxyInboundTag, deviceProxySelectorTag
	}
	return "device-proxy-" + id + "-in", "device-proxy-" + id + "-selector"
}

func isDeviceProxyInboundTag(tag string) bool {
	return tag == deviceProxyInboundTag ||
		(strings.HasPrefix(tag, "device-proxy-") && strings.HasSuffix(tag, "-in"))
}

func isDeviceProxySelectorTag(tag string) bool {
	return tag == deviceProxySelectorTag ||
		(strings.HasPrefix(tag, "device-proxy-") && strings.HasSuffix(tag, "-selector"))
}

// EnsureDeviceProxy writes (or overwrites) the inbound + selector
// outbound + route rule described by spec. Idempotent. Callers that
// toggle Enabled=false should use RemoveDeviceProxy.
//
// AWG-direct outbounds are NOT created or touched by this function —
// awgoutbounds owns 15-awg.json and tags from spec.AWGTags must
// resolve to outbounds already declared there. Legacy awg-* outbounds
// from pre-refactor builds are stripped on every call (idempotent).
func (c *Config) EnsureDeviceProxy(spec DeviceProxySpec) error {
	return c.EnsureDeviceProxyInstance(DeviceProxyInstanceSpec{
		ID:          "default",
		Enabled:     spec.Enabled,
		ListenAddr:  spec.ListenAddr,
		Port:        spec.Port,
		Auth:        spec.Auth,
		SelectedTag: spec.SelectedTag,
		AWGTags:     spec.AWGTags,
		SBTags:      spec.SBTags,
	})
}

// EnsureDeviceProxyInstance writes or overwrites one device-proxy instance.
// Non-default instances use unique tags derived from spec.ID.
func (c *Config) EnsureDeviceProxyInstance(spec DeviceProxyInstanceSpec) error {
	if !spec.Enabled {
		c.RemoveDeviceProxyInstance(spec.ID)
		return nil
	}

	// Inbound
	inboundTag, selectorTag := deviceProxyInstanceTags(spec.ID)

	inbound := map[string]any{
		"type":        "mixed",
		"tag":         inboundTag,
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
	c.upsertInbound(inboundTag, inbound)

	// Strip any legacy awg-* outbounds left over from the pre-15-awg.json
	// era. AWG outbounds now live in 15-awg.json.
	c.pruneAWGOutbounds(nil)

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
		"tag":       selectorTag,
		"outbounds": members,
	}
	if spec.SelectedTag != "" {
		selector["default"] = spec.SelectedTag
	}
	c.upsertOutbound(selectorTag, selector)

	c.ensureDeviceProxyInstanceRouteRule(inboundTag, selectorTag)
	return nil
}

// RemoveDeviceProxy strips every artefact EnsureDeviceProxy adds.
// Idempotent — safe on a config that never had the proxy.
func (c *Config) RemoveDeviceProxy() {
	c.RemoveDeviceProxyInstance("default")
}

// RemoveDeviceProxyInstance strips every artefact created for one instance.
func (c *Config) RemoveDeviceProxyInstance(id string) {
	inboundTag, selectorTag := deviceProxyInstanceTags(id)
	c.removeInbound(inboundTag)
	c.removeOutbound(selectorTag)
	c.pruneAWGOutbounds(nil)
	c.removeDeviceProxyInstanceRouteRule(inboundTag)
}

// RemoveAllDeviceProxyInstances strips every device-proxy inbound/selector/rule
// (default and named instances). Idempotent.
func (c *Config) RemoveAllDeviceProxyInstances() {
	inbounds := c.inbounds()
	nextInbounds := make([]any, 0, len(inbounds))
	for _, v := range inbounds {
		ib, ok := v.(map[string]any)
		if !ok {
			nextInbounds = append(nextInbounds, v)
			continue
		}
		tag, _ := ib["tag"].(string)
		if isDeviceProxyInboundTag(tag) {
			continue
		}
		nextInbounds = append(nextInbounds, v)
	}
	c.setInbounds(nextInbounds)

	outbounds := c.outbounds()
	nextOutbounds := make([]any, 0, len(outbounds))
	for _, v := range outbounds {
		ob, ok := v.(map[string]any)
		if !ok {
			nextOutbounds = append(nextOutbounds, v)
			continue
		}
		tag, _ := ob["tag"].(string)
		if isDeviceProxySelectorTag(tag) {
			continue
		}
		nextOutbounds = append(nextOutbounds, v)
	}
	c.setOutbounds(nextOutbounds)

	rules := c.routeRules()
	nextRules := make([]any, 0, len(rules))
	for _, v := range rules {
		r, ok := v.(map[string]any)
		if !ok {
			nextRules = append(nextRules, v)
			continue
		}
		inbound, _ := r["inbound"].(string)
		if isDeviceProxyInboundTag(inbound) {
			continue
		}
		nextRules = append(nextRules, v)
	}
	c.setRouteRules(nextRules)

	c.pruneAWGOutbounds(nil)
}

// HasDeviceProxy reports whether the config contains the device-proxy
// inbound (tag = deviceProxyInboundTag). Used by the slot migration
// to detect legacy single-file layouts where device-proxy lived inside
// 10-tunnels.json.
func (c *Config) HasDeviceProxy() bool {
	for _, v := range c.inbounds() {
		ib, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := ib["tag"].(string); t == deviceProxyInboundTag {
			return true
		}
	}
	return false
}

// ExtractDeviceProxy returns a NEW *Config containing ONLY the
// device-proxy artefacts (inbound, selector outbound, route rule)
// pulled from the receiver. The receiver is NOT modified — callers
// that want to strip the artefacts from the source must call
// RemoveDeviceProxy on it afterwards.
//
// The returned config is slot-shaped: it has inbounds/outbounds/route
// keys but NO log/dns/experimental defaults, so it can safely live in
// its own config.d slot file without colliding with 00-base.json.
func (c *Config) ExtractDeviceProxy() *Config {
	out := &Config{raw: map[string]any{
		"inbounds":  []any{},
		"outbounds": []any{},
		"route":     map[string]any{"rules": []any{}},
	}}
	for _, v := range c.inbounds() {
		ib, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := ib["tag"].(string); isDeviceProxyInboundTag(t) {
			out.upsertInbound(t, ib)
		}
	}
	for _, v := range c.outbounds() {
		ob, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := ob["tag"].(string); isDeviceProxySelectorTag(t) {
			out.upsertOutbound(t, ob)
		}
	}
	for _, v := range c.routeRules() {
		r, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if inbound, _ := r["inbound"].(string); isDeviceProxyInboundTag(inbound) {
			out.setRouteRules(append(out.routeRules(), r))
		}
	}
	return out
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
	c.ensureDeviceProxyInstanceRouteRule(deviceProxyInboundTag, deviceProxySelectorTag)
}

func (c *Config) ensureDeviceProxyInstanceRouteRule(inboundTag, selectorTag string) {
	rule := map[string]any{
		"inbound":  inboundTag,
		"outbound": selectorTag,
	}
	rules := c.routeRules()
	filtered := make([]any, 0, len(rules))
	for _, v := range rules {
		r, ok := v.(map[string]any)
		if !ok {
			filtered = append(filtered, v)
			continue
		}
		if r["inbound"] == inboundTag {
			continue
		}
		filtered = append(filtered, v)
	}
	c.setRouteRules(append([]any{rule}, filtered...))
}

func (c *Config) removeDeviceProxyRouteRule() {
	c.removeDeviceProxyInstanceRouteRule(deviceProxyInboundTag)
}

func (c *Config) removeDeviceProxyInstanceRouteRule(inboundTag string) {
	rules := c.routeRules()
	out := make([]any, 0, len(rules))
	for _, v := range rules {
		r, ok := v.(map[string]any)
		if !ok {
			out = append(out, v)
			continue
		}
		if r["inbound"] == inboundTag {
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
