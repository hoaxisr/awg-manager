//go:build linux

// Package wireguard controls the outer path of an existing kernel WireGuard
// interface. Provisioning and NDMS lifecycle remain the caller's responsibility.
package wireguard

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/uplink"
)

const routeProtocol = 196

// Config describes resources already exclusively reserved by the caller. The
// adapter never creates interfaces, changes keys, flushes routes or edits main.
// Only literal IPv4 endpoints are supported in this first kernel backend.
type Config struct {
	Ref, Interface, Peer  string
	BindingFile           string
	Endpoint              netip.AddrPort
	Mark                  uint32
	RetiringMark          uint32 // durable, owned NDMS mark handover; never guessed on observe
	Table, Priority       int
	TunnelDNS             []string
	AllowedIPs            []string
	LocalPublicKey        string
	KernelPhase           string // preparing, active or releasing; empty for native WG
	OriginalMark          uint32
	KernelPreviousBinding *bindingRecord
}

type Adapter struct {
	mu       sync.Mutex
	targets  map[string]Config
	run      Runner
	path     func(context.Context, string) (Path, error)
	peerMark func(context.Context, Config) (uint32, error)
}

func New(configs []Config, path func(context.Context, string) (Path, error), run Runner) (*Adapter, error) {
	if path == nil {
		return nil, errors.New("WireGuard path resolver is required")
	}
	if run == nil {
		run = defaultRunner
	}
	a := &Adapter{targets: map[string]Config{}, path: path, run: run}
	marks, tables, priorities, devices := map[uint32]bool{}, map[int]bool{}, map[int]bool{}, map[string]bool{}
	bindings := map[string]bool{}
	for _, c := range configs {
		if c.LocalPublicKey != "" {
			key, err := base64.StdEncoding.DecodeString(c.LocalPublicKey)
			if err != nil || len(key) != 32 {
				return nil, errors.New("invalid local WireGuard public identity")
			}
		}
		if !validPayloadPrefixes(c.AllowedIPs) {
			return nil, errors.New("unsupported WireGuard payload prefixes")
		}
		key, err := base64.StdEncoding.DecodeString(c.Peer)
		if err != nil || len(key) != 32 || len(c.Interface) == 0 || len(c.Interface) > 15 || strings.ContainsAny(c.Interface, "/\\ \t\r\n\x00") || strings.HasPrefix(c.Interface, "-") || !filepath.IsAbs(c.BindingFile) || c.Ref == "" || c.Mark == 0 || c.Table < 100 || c.Table >= 253 || c.Priority < 1 || c.Priority >= 32766 || !c.Endpoint.IsValid() || !c.Endpoint.Addr().Is4() || !c.Endpoint.Addr().IsGlobalUnicast() || c.Endpoint.Port() == 0 || len(c.TunnelDNS) == 0 {
			return nil, fmt.Errorf("invalid or unsupported WireGuard target %q", c.Ref)
		}
		for _, dns := range c.TunnelDNS {
			if _, err := uplink.ParseDNSServer(dns); err != nil {
				return nil, err
			}
		}
		if _, exists := a.targets[c.Ref]; exists || marks[c.Mark] || (c.RetiringMark != 0 && (c.RetiringMark == c.Mark || marks[c.RetiringMark])) || tables[c.Table] || priorities[c.Priority] || devices[c.Interface] || bindings[filepath.Clean(c.BindingFile)] {
			return nil, errors.New("WireGuard targets must have exclusive devices, marks, tables and priorities")
		}
		marks[c.Mark], tables[c.Table], priorities[c.Priority], devices[c.Interface] = true, true, true, true
		if c.RetiringMark != 0 {
			marks[c.RetiringMark] = true
		}
		bindings[filepath.Clean(c.BindingFile)] = true
		c.TunnelDNS = append([]string(nil), c.TunnelDNS...)
		c.AllowedIPs = append([]string(nil), c.AllowedIPs...)
		a.targets[c.Ref] = c
	}
	return a, nil
}

func (*Adapter) UplinkCapabilities(uplink.Target) uplink.Capabilities {
	return uplink.Capabilities{RestartScope: "target", Limitation: "pre-provisioned Linux kernel WireGuard; literal IPv4 endpoint; manual failback; NDMS/AWG proxy integration unavailable"}
}

func (a *Adapter) config(t uplink.Target) (Config, error) {
	c, ok := a.targets[t.Ref]
	if t.Engine != "wireguard" || t.Kind != "" {
		return c, fmt.Errorf("%w: Adapter target engine=%q kind=%q", uplink.ErrUnsupported, t.Engine, t.Kind)
	}
	if !ok {
		return c, uplink.ErrNotFound
	}
	return c, nil
}

type route struct {
	Type, Dst, Dev, Gateway, Prefsrc string
	Protocol                         any
	Table                            any
	Metric                           uint64
	Extra                            map[string]json.RawMessage `json:"-"`
}

func (r *route) UnmarshalJSON(data []byte) error {
	type plain route
	if err := json.Unmarshal(data, (*plain)(r)); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, key := range []string{"type", "dst", "dev", "gateway", "prefsrc", "protocol", "table", "metric", "scope", "flags", "pref", "uid", "cache", "mark"} {
		delete(fields, key)
	}
	if len(fields) > 0 {
		r.Extra = fields
	}
	return nil
}

type snapshot struct {
	Device           string
	Enabled          bool
	NeedsRebind      bool
	Routes           []route
	MissingRules     []string
	MissingTerminals []string
	RetiringRules    []string
}

type bindingRecord struct {
	Version int    `json:"version"`
	Device  string `json:"device"`
	Source  string `json:"source"`
	Gateway string `json:"gateway"`
	Direct  bool   `json:"direct,omitempty"`
}

func readBinding(path string) (bindingRecord, error) {
	var r bindingRecord
	b, err := os.ReadFile(path)
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal(b, &r); err != nil {
		return r, err
	}
	if !validBindingRecord(r) {
		return r, errors.New("invalid durable WireGuard WAN binding")
	}
	return r, nil
}
func validBindingRecord(r bindingRecord) bool {
	gw, err := netip.ParseAddr(r.Gateway)
	validGateway := err == nil && gw.Is4() && gw.IsGlobalUnicast() && !r.Direct
	return (validGateway || (r.Direct && r.Gateway == "")) && r.Version == 1 && (uplink.Binding{Device: r.Device, IPv4: r.Source}).Validate() == nil
}

func writeBinding(path string, r bindingRecord) error {
	return writeJSON(path, r)
}
func writeJSON(path string, r any) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if err := storage.AtomicWritePerm(path, b, 0600); err != nil {
		return err
	}
	// storage глотает отказ fsync каталога, а через writeJSON пишутся привязка
	// и реестр, после которых меняются маршруты: EIO здесь обязан остановить.
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (a *Adapter) read(ctx context.Context, c Config) (snapshot, error) {
	return a.readResources(ctx, c, false)
}
func (a *Adapter) readResources(ctx context.Context, c Config, allowMissing bool) (snapshot, error) {
	return a.auditResources(ctx, c, allowMissing, true)
}
func (a *Adapter) auditResources(ctx context.Context, c Config, allowMissing, inspectKernel bool) (snapshot, error) {
	return a.auditBinding(ctx, c, allowMissing, inspectKernel, nil)
}
func (a *Adapter) auditBinding(ctx context.Context, c Config, allowMissing, inspectKernel bool, previous *bindingRecord) (snapshot, error) {
	var s snapshot
	record, err := readBinding(c.BindingFile)
	if err != nil {
		return s, err
	}
	if previous != nil {
		if !validBindingRecord(*previous) {
			return s, uplink.ErrConflict
		}
		record = *previous
	}
	s.Device = record.Device
	if inspectKernel {
		iface, err := net.InterfaceByName(c.Interface)
		if err != nil {
			return s, err
		}
		s.Enabled = iface.Flags&net.FlagUp != 0
		expected := map[string]string{"peers": c.Peer, "endpoints": c.Peer + "\t" + c.Endpoint.String(), "allowed-ips": c.Peer + "\t" + payloadPrefixes(c.AllowedIPs)}
		if c.LocalPublicKey != "" {
			expected["public-key"] = c.LocalPublicKey
		}
		if a.peerMark == nil {
			expected["fwmark"] = fmt.Sprintf("0x%x", c.Mark)
		} else {
			mark, e := a.peerMark(ctx, c)
			if e != nil {
				return s, e
			}
			if mark != c.Mark {
				return s, fmt.Errorf("%w: native peer mark changed", uplink.ErrConflict)
			}
		}
		for field, want := range expected {
			out, err := a.run(ctx, "wg", "show", c.Interface, field)
			if err != nil {
				return s, fmt.Errorf("inspect WireGuard %s: %w", field, err)
			}
			if strings.TrimSpace(string(out)) != want {
				return s, fmt.Errorf("%w: WireGuard %s changed or unsupported", uplink.ErrConflict, field)
			}
		}
	}
	// An unreachable default terminates the marked lookup even after the WAN
	// route disappears. Without it the kernel would continue into main.
	for _, family := range []string{"-4", "-6"} {
		out, err := a.run(ctx, "ip", family, "-j", "rule", "show")
		if err != nil {
			return s, err
		}
		var rules []map[string]any
		if err = json.Unmarshal(out, &rules); err != nil {
			return s, err
		}
		matches, retiringMatches := 0, 0
		for _, r := range rules {
			priority, validPriority := r["priority"].(float64)
			if !validPriority {
				return s, uplink.ErrConflict
			}
			mark, hasMark := r["fwmark"].(string)
			parsed, parseErr := strconv.ParseUint(mark, 0, 32)
			if hasMark && parseErr != nil {
				return s, uplink.ErrConflict
			}
			mask := uint64(0xffffffff)
			if value, present := r["fwmask"]; present {
				raw, ok := value.(string)
				if !ok {
					return s, uplink.ErrConflict
				}
				var e error
				mask, e = strconv.ParseUint(raw, 0, 32)
				if e != nil {
					return s, uplink.ErrConflict
				}
			}
			if int(priority) < c.Priority && !hasMark && !(priority == 0 && r["table"] == "local" && r["src"] == "all" && len(r) == 3) {
				return s, fmt.Errorf("%w: earlier non-mark routing rule", uplink.ErrConflict)
			}
			if int(priority) < c.Priority && hasMark {
				for key, value := range r {
					switch key {
					case "priority", "fwmark", "fwmask", "table", "protocol":
					case "src":
						if value != "all" {
							return s, uplink.ErrConflict
						}
					default:
						return s, uplink.ErrConflict
					}
				}
			}
			if fmt.Sprint(r["table"]) == strconv.Itoa(c.Table) && int(priority) != c.Priority {
				return s, uplink.ErrConflict
			}
			overlaps := hasMark && (parsed&mask == uint64(c.Mark)&mask || (c.RetiringMark != 0 && parsed&mask == uint64(c.RetiringMark)&mask))
			if int(priority) == c.Priority || (int(priority) < c.Priority && overlaps) {
				ownedMark := uint32(parsed) == c.Mark || (c.RetiringMark != 0 && uint32(parsed) == c.RetiringMark)
				if int(priority) != c.Priority || !hasMark || !ownedMark || fmt.Sprint(r["table"]) != strconv.Itoa(c.Table) || (r["fwmask"] != nil && r["fwmask"] != "0xffffffff") || (r["action"] != nil && r["action"] != "to_tbl") || len(r) > 7 {
					return s, fmt.Errorf("%w: foreign WireGuard routing rule", uplink.ErrConflict)
				}
				// Disallow selectors that could let some marked packets escape.
				for k, v := range r {
					switch k {
					case "priority", "fwmark", "fwmask", "table", "protocol":
					case "src":
						if v != "all" {
							return s, uplink.ErrConflict
						}
					default:
						return s, uplink.ErrConflict
					}
				}
				if uint32(parsed) == c.Mark {
					matches++
				} else {
					retiringMatches++
				}
			}
		}
		if retiringMatches > 1 {
			return s, uplink.ErrConflict
		}
		if retiringMatches == 1 {
			s.RetiringRules = append(s.RetiringRules, family)
		}
		if matches == 0 && allowMissing {
			s.MissingRules = append(s.MissingRules, family)
		} else if matches != 1 {
			return s, errors.New("missing exclusive WireGuard mark rule")
		}
		queryTable := strconv.Itoa(c.Table)
		if allowMissing {
			queryTable = "all"
		}
		out, err = a.run(ctx, "ip", family, "-j", "route", "show", "table", queryTable)
		if err != nil {
			return s, err
		}
		var routes []route
		if err = json.Unmarshal(out, &routes); err != nil {
			return s, err
		}
		if allowMissing {
			filtered := routes[:0]
			for _, r := range routes {
				if fmt.Sprint(r.Table) == strconv.Itoa(c.Table) {
					filtered = append(filtered, r)
				}
			}
			routes = filtered
		}
		terminal, active := 0, 0
		for _, r := range routes {
			if len(r.Extra) > 0 || r.Dst != "default" || fmt.Sprint(r.Protocol) != strconv.Itoa(routeProtocol) {
				return s, fmt.Errorf("%w: foreign WireGuard table route", uplink.ErrConflict)
			}
			switch {
			case r.Type == "unreachable" && r.Metric == 32767:
				terminal++
			case family == "-4" && (r.Type == "" || r.Type == "unicast") && r.Metric == 10 && r.Dev != "" && (r.Gateway != "" || record.Direct) && r.Prefsrc != "":
				active++
				if r.Dev != record.Device || r.Prefsrc != record.Source || r.Gateway != record.Gateway {
					return s, fmt.Errorf("%w: route disagrees with durable WireGuard binding", uplink.ErrConflict)
				}
			default:
				return s, fmt.Errorf("%w: unrecognized %s WireGuard table route (%+v)", uplink.ErrConflict, family, r)
			}
		}
		if terminal == 0 && allowMissing {
			s.MissingTerminals = append(s.MissingTerminals, family)
		} else if terminal != 1 {
			return s, errors.New("WireGuard table lacks its terminal unreachable route")
		}
		if active > 1 {
			return s, fmt.Errorf("%w: duplicate %s active WireGuard routes (%d)", uplink.ErrConflict, family, active)
		}
		if family == "-4" {
			s.Routes = routes
			s.NeedsRebind = active == 0 || uplink.BindingNeedsRefresh(uplink.Binding{Device: record.Device, IPv4: record.Source})
		}
	}
	return s, nil
}

func (a *Adapter) Validate(ctx context.Context, t uplink.Target) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, err := a.config(t)
	if err != nil {
		return err
	}
	_, err = a.read(ctx, c)
	return err
}
func (a *Adapter) Observe(ctx context.Context, t uplink.Target) (uplink.Observation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, err := a.config(t)
	if err != nil {
		return uplink.Observation{}, err
	}
	s, err := a.read(ctx, c)
	return uplink.Observation{Device: s.Device, Enabled: s.Enabled, NeedsRebind: s.NeedsRebind}, err
}

func (a *Adapter) Activate(ctx context.Context, t uplink.Target, old, next string) error {
	return a.activate(ctx, t, old, next, false)
}
func (a *Adapter) activate(ctx context.Context, t uplink.Target, old, next string, allowDown bool) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, err := a.config(t)
	if err != nil {
		return err
	}
	s, err := a.read(ctx, c)
	if err != nil {
		return err
	}
	if (!s.Enabled && !allowDown) || s.Device != old {
		return fmt.Errorf("%w: Adapter activate enabled=%v device=%q old=%q", uplink.ErrConflict, s.Enabled, s.Device, old)
	}
	p, err := a.path(ctx, next)
	if err != nil {
		return fmt.Errorf("%w: %v", uplink.ErrPathUnavailable, err)
	}
	validGateway := p.Gateway.Is4() && p.Gateway.IsGlobalUnicast() && !p.Direct
	if p.Direct {
		iface, e := net.InterfaceByName(next)
		validGateway = e == nil && iface.Flags&net.FlagPointToPoint != 0 && !p.Gateway.IsValid()
	}
	if p.Binding.Device != next || p.Binding.Validate() != nil || p.Binding.IPv4 == "" || !validGateway {
		return fmt.Errorf("%w: invalid explicit WAN gateway/source", uplink.ErrPathUnavailable)
	}
	// No endpoint or peer key changes: the same authenticated session may roam
	// to the new external source. Only this tunnel's reserved table changes.
	// Commit intent first. A crash with an old route still present produces an
	// explicit conflict; a vanished route retains the intended WAN identity.
	if err = writeBinding(c.BindingFile, recordForPath(p)); err != nil {
		return err
	}
	args := []string{"-4", "route", "replace", "default"}
	if !p.Direct {
		args = append(args, "via", p.Gateway.String())
	}
	args = append(args, "dev", next, "src", p.Binding.IPv4, "table", strconv.Itoa(c.Table), "proto", strconv.Itoa(routeProtocol), "metric", "10")
	_, err = a.run(ctx, "ip", args...)
	if err != nil {
		return err
	}
	return a.verifyPath(ctx, c, next)
}

func recordForPath(p Path) bindingRecord {
	r := bindingRecord{Version: 1, Device: p.Binding.Device, Source: p.Binding.IPv4, Direct: p.Direct}
	if p.Gateway.IsValid() {
		r.Gateway = p.Gateway.String()
	}
	return r
}

func (a *Adapter) verifyPath(ctx context.Context, c Config, device string) error {
	out, err := a.run(ctx, "ip", "-4", "-j", "route", "get", c.Endpoint.Addr().String(), "mark", fmt.Sprintf("0x%x", c.Mark))
	if err != nil {
		return err
	}
	var routes []route
	if err = json.Unmarshal(out, &routes); err != nil {
		return err
	}
	record, err := readBinding(c.BindingFile)
	if err != nil {
		return err
	}
	if len(routes) != 1 || routes[0].Dev != device || fmt.Sprint(routes[0].Table) != strconv.Itoa(c.Table) || routes[0].Prefsrc != record.Source {
		return fmt.Errorf("%w: WireGuard outer route uses another device", uplink.ErrConflict)
	}
	return nil
}

func (a *Adapter) Probe(ctx context.Context, t uplink.Target, device string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	c, err := a.config(t)
	if err != nil {
		return err
	}
	before, err := a.read(ctx, c)
	if err != nil {
		return err
	}
	if before.Device != device || !before.Enabled {
		return fmt.Errorf("%w: Probe before mismatch device=%q want=%q enabled=%v", uplink.ErrConflict, before.Device, device, before.Enabled)
	}
	if err = a.verifyPath(ctx, c, device); err != nil {
		return err
	}
	binding, err := uplink.InspectBinding(c.Interface)
	if err != nil {
		return err
	}
	binding.DNSServers = c.TunnelDNS
	if err = uplink.ProbeBinding(ctx, binding); err != nil {
		return err
	}
	after, err := a.read(ctx, c)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(before, after) {
		return fmt.Errorf("%w: Probe snapshot changed during check", uplink.ErrConflict)
	}
	return a.verifyPath(ctx, c, device)
}
