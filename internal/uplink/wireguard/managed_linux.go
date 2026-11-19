//go:build linux

package wireguard

import (
	"context"
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
	"sync/atomic"

	"github.com/hoaxisr/awg-manager/internal/uplink"
)

type Managed struct {
	mu        sync.Mutex
	deps      ManagedDeps
	profiles  map[string]Config
	registry  string
	protected atomic.Value
}
type registry struct {
	Version  int               `json:"version"`
	Profiles map[string]Config `json:"profiles"`
}

func NewManaged(d ManagedDeps) (*Managed, error) {
	if d.Directory == "" || d.Lookup == nil || d.List == nil || d.Providers == nil || d.WithLock == nil || d.ApplyBinding == nil {
		return nil, errors.New("incomplete native uplink dependencies")
	}
	if (d.PeerMark == nil) != (d.PeerMarks == nil) {
		return nil, errors.New("native peer mark reader and catalogue must be provided together")
	}
	if d.Run == nil {
		d.Run = defaultRunner
	}
	absolute, err := filepath.Abs(d.Directory)
	if err != nil {
		return nil, err
	}
	d.Directory = absolute
	m := &Managed{deps: d, profiles: map[string]Config{}, registry: filepath.Join(d.Directory, "uplink-wireguard.json")}
	raw, err := os.ReadFile(m.registry)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		var r registry
		if err = json.Unmarshal(raw, &r); err != nil {
			return nil, err
		}
		if (r.Version < 1 || r.Version > 4) || r.Profiles == nil {
			return nil, errors.New("unsupported native uplink registry")
		}
		m.profiles = r.Profiles
	}
	// Restored archives cannot redirect binding writes outside the data dir.
	for ref, c := range m.profiles {
		if ref != c.Ref || filepath.Base(c.BindingFile) != filepath.Base(m.bindingPath(c.Ref)) {
			return nil, errors.New("invalid native uplink registry path")
		}
		c.BindingFile = m.bindingPath(ref)
		m.profiles[ref] = c
	}
	if _, err = m.kernel(); err != nil {
		return nil, err
	}
	m.publishProtection()
	return m, nil
}
func (m *Managed) publishProtection() {
	refs := map[string]bool{}
	for ref := range m.profiles {
		refs[ref] = true
	}
	m.protected.Store(refs)
}

// OwnsTarget is lock-free because storage guards call it under a file lock.
func (m *Managed) OwnsTarget(ref string) bool {
	v := m.protected.Load()
	if v == nil {
		return false
	}
	return v.(map[string]bool)[ref]
}

func (m *Managed) bindingPath(ref string) string {
	// Hex encoding keeps opaque tunnel IDs from becoming filesystem paths.
	return filepath.Join(m.deps.Directory, "uplink-wireguard-bindings", fmt.Sprintf("%x.json", []byte(ref)))
}
func (m *Managed) save() error {
	if err := os.MkdirAll(m.deps.Directory, 0700); err != nil {
		return err
	}
	profiles := map[string]Config{}
	for ref, c := range m.profiles {
		c.BindingFile = filepath.Base(c.BindingFile)
		profiles[ref] = c
	}
	return writeJSON(m.registry, registry{Version: 4, Profiles: profiles})
}
func (m *Managed) kernel() (*Adapter, error) {
	configs := make([]Config, 0, len(m.profiles))
	for _, c := range m.profiles {
		configs = append(configs, c)
	}
	a, err := New(configs, m.path, m.deps.Run)
	if err == nil {
		m.configurePeerMark(a)
	}
	return a, err
}
func (m *Managed) configurePeerMark(a *Adapter) {
	if m.deps.PeerMark != nil {
		a.peerMark = func(ctx context.Context, c Config) (uint32, error) {
			t := ManagedTarget{Ref: c.Ref, Interface: c.Interface, Peer: c.Peer, Endpoint: c.Endpoint.String()}
			mark, err := m.deps.PeerMark(ctx, t)
			if err != nil {
				return 0, err
			}
			if err = m.uniqueMark(ctx, t, mark); err != nil {
				return 0, err
			}
			return mark, nil
		}
	}
}

func (m *Managed) uniqueMark(ctx context.Context, t ManagedTarget, mark uint32) error {
	if mark == 0 {
		return fmt.Errorf("%w: zero peer mark in uniqueMark", uplink.ErrUnsupported)
	}
	for ref, c := range m.profiles {
		if ref != t.Ref && (c.Mark == mark || c.RetiringMark == mark) {
			return fmt.Errorf("%w: mark 0x%x already used by %s", uplink.ErrConflict, mark, ref)
		}
	}
	if m.deps.PeerMarks != nil {
		catalogue, err := m.deps.PeerMarks(ctx)
		if err != nil {
			return err
		}
		count := 0
		for iface, marks := range catalogue {
			for _, v := range marks {
				if v == mark {
					if iface != t.Interface {
						return fmt.Errorf("%w: native peer mark is shared", uplink.ErrConflict)
					}
					count++
				}
			}
		}
		if count != 1 {
			return fmt.Errorf("%w: mark 0x%x count=%d for %s in NDMS peers", uplink.ErrConflict, mark, count, t.Interface)
		}
		return nil
	}
	return nil
}
func (*Managed) UplinkCapabilities(uplink.Target) uplink.Capabilities {
	return uplink.Capabilities{IndependentEngine: true, RestartScope: "target", ControlledFailback: true, Limitation: "native NDMS/ASC, literal IPv4 endpoint and explicit tunnel DNS; manual or controlled return; device acceptance pending"}
}

func (m *Managed) UplinkTargets(ctx context.Context) ([]uplink.TargetInfo, error) {
	list, err := m.deps.List(ctx)
	if err != nil {
		return nil, err
	}
	var result []uplink.TargetInfo
	for _, t := range list {
		if eligible(t) == nil {
			result = append(result, uplink.TargetInfo{Target: uplink.Target{Engine: "wireguard", Ref: t.Ref}, Label: t.Label + " · native WireGuard/ASC"})
		}
	}
	return result, nil
}
func eligible(t ManagedTarget) error {
	if !validPayloadPrefixes(t.AllowedIPs) {
		return fmt.Errorf("%w: unsupported allowed IPs %v", uplink.ErrUnsupported, t.AllowedIPs)
	}
	endpoint, err := netip.ParseAddrPort(t.Endpoint)
	if err != nil || !endpoint.Addr().Is4() || !endpoint.Addr().IsGlobalUnicast() || endpoint.Port() == 0 || t.Ref == "" || t.Interface == "" || t.Peer == "" || len(t.TunnelDNS) == 0 {
		return fmt.Errorf("%w: native uplink needs one literal IPv4 peer and explicit tunnel DNS", uplink.ErrUnsupported)
	}
	for _, dns := range t.TunnelDNS {
		if _, err := uplink.ParseDNSServer(dns); err != nil {
			return fmt.Errorf("%w: %v", uplink.ErrUnsupported, err)
		}
	}
	return nil
}
func (m *Managed) target(ctx context.Context, t uplink.Target) (ManagedTarget, error) {
	if t.Engine != "wireguard" || t.Kind != "" {
		return ManagedTarget{}, fmt.Errorf("%w: unexpected target engine=%q kind=%q", uplink.ErrUnsupported, t.Engine, t.Kind)
	}
	v, err := m.deps.Lookup(ctx, t.Ref)
	if err != nil {
		return v, err
	}
	return v, eligible(v)
}
func (m *Managed) compatible(t ManagedTarget, c Config) error {
	if t.Interface != c.Interface || t.Peer != c.Peer || t.Endpoint != c.Endpoint.String() || !reflect.DeepEqual(t.TunnelDNS, c.TunnelDNS) || payloadPrefixes(t.AllowedIPs) != payloadPrefixes(c.AllowedIPs) || t.LocalPublicKey != c.LocalPublicKey {
		return fmt.Errorf("%w: native uplink target configuration changed", uplink.ErrConflict)
	}
	return nil
}

func settledMark(c Config) error {
	if c.RetiringMark != 0 {
		return fmt.Errorf("%w: interrupted native mark handover; disable policy to clean up", uplink.ErrConflict)
	}
	return nil
}
func (m *Managed) locked(ctx context.Context, t uplink.Target, fn func(ManagedTarget) error) error {
	return m.deps.WithLock(ctx, t.Ref, func() error {
		m.mu.Lock()
		defer m.mu.Unlock()
		v, err := m.target(ctx, t)
		if err != nil {
			return err
		}
		return fn(v)
	})
}
func (m *Managed) Validate(ctx context.Context, t uplink.Target) error {
	return m.locked(ctx, t, func(v ManagedTarget) error {
		if c, ok := m.profiles[t.Ref]; ok {
			if err := settledMark(c); err != nil {
				return err
			}
			if err := m.compatible(v, c); err != nil {
				return err
			}
			if !v.Enabled {
				return nil
			}
			a, err := m.kernel()
			if err != nil {
				return err
			}
			_, err = a.readResources(ctx, c, true)
			return err
		}
		if !v.Enabled {
			return nil
		}
		_, err := m.mark(ctx, v)
		return err
	})
}
func (m *Managed) Observe(ctx context.Context, t uplink.Target) (uplink.Observation, error) {
	var ob uplink.Observation
	err := m.locked(ctx, t, func(v ManagedTarget) error {
		ob.Enabled = v.Enabled
		if !v.Enabled {
			return nil
		}
		if c, ok := m.profiles[t.Ref]; ok {
			if err := settledMark(c); err != nil {
				return err
			}
			if err := m.compatible(v, c); err != nil {
				return err
			}
			a, err := m.kernel()
			if err != nil {
				return err
			}
			s, err := a.readResources(ctx, c, true)
			ob.Device = s.Device
			ob.NeedsRebind = s.NeedsRebind || !s.Enabled || len(s.MissingRules) > 0 || len(s.MissingTerminals) > 0
			return err
		}
		mark, err := m.mark(ctx, v)
		if err != nil {
			return err
		}
		out, err := m.deps.Run(ctx, "ip", "-4", "-j", "route", "get", netip.MustParseAddrPort(v.Endpoint).Addr().String(), "mark", fmt.Sprintf("0x%x", mark))
		if err != nil {
			return err
		}
		var routes []route
		if err = json.Unmarshal(out, &routes); err != nil {
			return err
		}
		if len(routes) != 1 {
			return fmt.Errorf("%w: Observe routes len=%d", uplink.ErrUnsupported, len(routes))
		}
		for _, p := range m.deps.Providers() {
			if p.Device == routes[0].Dev {
				ob.Device = p.Device
				return nil
			}
		}
		return errors.New("native endpoint route does not identify a known WAN")
	})
	return ob, err
}
func (m *Managed) mark(ctx context.Context, t ManagedTarget) (uint32, error) {
	var mark uint64
	var out []byte
	var err error
	if m.deps.PeerMark != nil {
		value, e := m.deps.PeerMark(ctx, t)
		if e != nil {
			return 0, e
		}
		mark = uint64(value)
	} else {
		out, err = m.deps.Run(ctx, "wg", "show", t.Interface, "fwmark")
		if err != nil {
			return 0, err
		}
		mark, err = strconv.ParseUint(strings.TrimSpace(string(out)), 0, 32)
		if err != nil {
			return 0, err
		}
	}
	if mark == 0 {
		return 0, fmt.Errorf("%w: runtime fwmark is 0 on %s", uplink.ErrUnsupported, t.Interface)
	}
	if t.LocalPublicKey != "" {
		out, err = m.deps.Run(ctx, "wg", "show", t.Interface, "public-key")
		if err != nil {
			return 0, err
		}
		if strings.TrimSpace(string(out)) != t.LocalPublicKey {
			return 0, fmt.Errorf("%w: local public-key changed on %s", uplink.ErrConflict, t.Interface)
		}
	}
	for field, want := range map[string]string{"peers": t.Peer, "endpoints": t.Peer + "\t" + t.Endpoint, "allowed-ips": t.Peer + "\t" + payloadPrefixes(t.AllowedIPs)} {
		out, err = m.deps.Run(ctx, "wg", "show", t.Interface, field)
		if err != nil {
			return 0, err
		}
		if strings.TrimSpace(string(out)) != want {
			return 0, fmt.Errorf("%w: native kernel %s changed", uplink.ErrConflict, field)
		}
	}
	if m.deps.PeerMarks != nil {
		if err = m.uniqueMark(ctx, t, uint32(mark)); err != nil {
			return 0, err
		}
		return uint32(mark), nil
	}
	out, err = m.deps.Run(ctx, "wg", "show", "all", "fwmark")
	if err != nil {
		return 0, err
	}
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.Fields(line)
		if len(parts) != 2 {
			return 0, fmt.Errorf("%w: unexpected wg show all fwmark line %q", uplink.ErrUnsupported, line)
		}
		value, e := strconv.ParseUint(parts[1], 0, 32)
		if e != nil {
			if parts[1] == "off" {
				continue
			}
			return 0, e
		}
		if value == mark {
			if parts[0] != t.Interface {
				return 0, fmt.Errorf("%w: kernel mark is shared", uplink.ErrConflict)
			}
			count++
		}
	}
	if count != 1 {
		return 0, fmt.Errorf("%w: kernel mark 0x%x count=%d on %s", uplink.ErrConflict, mark, count, t.Interface)
	}
	return uint32(mark), nil
}
func (m *Managed) allocate(ctx context.Context, v ManagedTarget, mark uint32) (Config, error) {
	c := Config{Ref: v.Ref, Interface: v.Interface, Peer: v.Peer, Endpoint: netip.MustParseAddrPort(v.Endpoint), Mark: mark, TunnelDNS: append([]string(nil), v.TunnelDNS...), BindingFile: m.bindingPath(v.Ref)}
	c.AllowedIPs = append([]string(nil), v.AllowedIPs...)
	c.LocalPublicKey = v.LocalPublicKey
	tables, priorities := map[int]bool{}, map[int]bool{}
	for _, p := range m.profiles {
		if p.Interface == v.Interface {
			return c, fmt.Errorf("%w: interface already owned by another target", uplink.ErrConflict)
		}
		if p.Mark == mark {
			return c, fmt.Errorf("%w: mark 0x%x already owned by %s", uplink.ErrConflict, mark, p.Ref)
		}
		tables[p.Table] = true
		priorities[p.Priority] = true
	}
	for _, family := range []string{"-4", "-6"} {
		out, err := m.deps.Run(ctx, "ip", family, "-j", "rule", "show")
		if err != nil {
			return c, err
		}
		var rules []map[string]any
		if err = json.Unmarshal(out, &rules); err != nil {
			return c, err
		}
		for _, r := range rules {
			if n, ok := r["priority"].(float64); ok {
				priorities[int(n)] = true
			}
			if n, e := strconv.Atoi(fmt.Sprint(r["table"])); e == nil {
				tables[n] = true
			}
		}
		out, err = m.deps.Run(ctx, "ip", family, "-j", "route", "show", "table", "all")
		if err != nil {
			return c, err
		}
		var rows []map[string]any
		if err = json.Unmarshal(out, &rows); err != nil {
			return c, err
		}
		for _, r := range rows {
			if n, e := strconv.Atoi(fmt.Sprint(r["table"])); e == nil {
				tables[n] = true
			}
		}
	}
	for n := 100; n < 253; n++ {
		if !tables[n] {
			c.Table = n
			break
		}
	}
	for n := 1; n < 100; n++ {
		if !priorities[n] {
			c.Priority = n
			break
		}
	}
	if c.Table == 0 || c.Priority == 0 {
		return c, errors.New("no exclusive native uplink routing resources available")
	}
	return c, nil
}
func (m *Managed) ensure(ctx context.Context, c Config) error {
	return m.ensureInspected(ctx, c, true)
}
func (m *Managed) ensureInspected(ctx context.Context, c Config, inspect bool) error {
	a, err := m.kernel()
	if err != nil {
		return err
	}
	s, err := a.auditResources(ctx, c, true, inspect)
	if err != nil {
		return err
	}
	for _, family := range s.MissingTerminals {
		if _, err = m.deps.Run(ctx, "ip", family, "route", "add", "unreachable", "default", "table", strconv.Itoa(c.Table), "proto", strconv.Itoa(routeProtocol), "metric", "32767"); err != nil {
			return err
		}
	}
	for _, family := range s.MissingRules {
		if _, err = m.deps.Run(ctx, "ip", family, "rule", "add", "pref", strconv.Itoa(c.Priority), "fwmark", fmt.Sprintf("0x%x", c.Mark), "lookup", strconv.Itoa(c.Table)); err != nil {
			return err
		}
	}
	return nil
}
func (m *Managed) path(ctx context.Context, device string) (Path, error) {
	if m.deps.ResolvePath != nil {
		return m.deps.ResolvePath(ctx, device)
	}
	binding, err := uplink.InspectBinding(device)
	if err != nil {
		return Path{}, err
	}
	out, err := m.deps.Run(ctx, "ip", "-4", "-j", "route", "show", "table", "all")
	if err != nil {
		return Path{}, err
	}
	var rows []route
	if err = json.Unmarshal(out, &rows); err != nil {
		return Path{}, err
	}
	var gateway netip.Addr
	direct := false
	for _, r := range rows {
		if r.Dev != device || r.Dst != "default" || (r.Type != "" && r.Type != "unicast") || fmt.Sprint(r.Protocol) == strconv.Itoa(routeProtocol) {
			continue
		}
		g, e := netip.ParseAddr(r.Gateway)
		if r.Gateway == "" {
			iface, err := net.InterfaceByName(device)
			if err == nil && iface.Flags&net.FlagPointToPoint != 0 {
				if gateway.IsValid() {
					return Path{}, errors.New("ambiguous point-to-point gateway")
				}
				direct = true
			}
			continue
		}
		if e != nil || !g.Is4() || !g.IsGlobalUnicast() {
			continue
		}
		if direct || (gateway.IsValid() && gateway != g) {
			return Path{}, errors.New("ambiguous provider gateways")
		}
		gateway = g
	}
	if !gateway.IsValid() && !direct {
		return Path{}, errors.New("provider has no explicit IPv4 gateway")
	}
	return Path{Binding: binding, Gateway: gateway, Direct: direct}, nil
}
func (m *Managed) provider(device string) (uplink.Provider, error) {
	for _, p := range m.deps.Providers() {
		if p.Device == device {
			return p, nil
		}
	}
	return uplink.Provider{}, errors.New("unknown native uplink provider")
}

func (m *Managed) Activate(ctx context.Context, t uplink.Target, old, next string) error {
	return m.locked(ctx, t, func(v ManagedTarget) error {
		if !v.Enabled {
			return fmt.Errorf("%w: tunnel %s is disabled", uplink.ErrConflict, v.Ref)
		}
		p, err := m.provider(next)
		if err != nil || !p.Up {
			return uplink.ErrPathUnavailable
		}
		path, err := m.path(ctx, next)
		if err != nil {
			return fmt.Errorf("%w: %v", uplink.ErrPathUnavailable, err)
		}
		c, exists := m.profiles[t.Ref]
		if !exists {
			mark, err := m.mark(ctx, v)
			if err != nil {
				return err
			}
			// Compare against the actual old route before reserving anything.
			out, e := m.deps.Run(ctx, "ip", "-4", "-j", "route", "get", netip.MustParseAddrPort(v.Endpoint).Addr().String(), "mark", fmt.Sprintf("0x%x", mark))
			if e != nil {
				return e
			}
			var routes []route
			if e = json.Unmarshal(out, &routes); e != nil {
				return e
			}
			if len(routes) != 1 || routes[0].Dev != old {
				return fmt.Errorf("%w: Activate expected route via %q, got len=%d (%+v)", uplink.ErrConflict, old, len(routes), routes)
			}
			c, err = m.allocate(ctx, v, mark)
			if err != nil {
				return err
			}
			if err = os.MkdirAll(filepath.Dir(c.BindingFile), 0700); err != nil {
				return err
			}
			if err = writeBinding(c.BindingFile, recordForPath(path)); err != nil {
				return err
			}
			preflight, err := New([]Config{c}, m.path, m.deps.Run)
			if err != nil {
				os.Remove(c.BindingFile)
				return err
			}
			m.configurePeerMark(preflight)
			if _, err = preflight.readResources(ctx, c, true); err != nil {
				os.Remove(c.BindingFile)
				return err
			}
			m.profiles[t.Ref] = c
			if err = m.save(); err != nil {
				delete(m.profiles, t.Ref)
				return err
			}
			m.publishProtection()
			old = next
		} else {
			if err := settledMark(c); err != nil {
				return err
			}
			if err := m.compatible(v, c); err != nil {
				return err
			}
			r, err := readBinding(c.BindingFile)
			if err != nil {
				return err
			}
			if r.Device != old {
				return fmt.Errorf("%w: durable binding device %q disagrees with old=%q", uplink.ErrConflict, r.Device, old)
			}
		}
		if err = m.ensure(ctx, c); err != nil {
			return err
		}
		a, err := m.kernel()
		if err != nil {
			return err
		}
		if err = a.activate(ctx, t, old, next, true); err != nil {
			return err
		}
		if err = m.deps.ApplyBinding(ctx, t.Ref, p.ID, next); err != nil {
			return err
		}
		return m.adoptAppliedMark(ctx, v, c, next)
	})
}
func (m *Managed) Probe(ctx context.Context, t uplink.Target, device string) error {
	return m.locked(ctx, t, func(v ManagedTarget) error {
		if !v.Enabled {
			return fmt.Errorf("%w: Probe tunnel %s is disabled", uplink.ErrConflict, v.Ref)
		}
		c, ok := m.profiles[t.Ref]
		if !ok {
			return fmt.Errorf("%w: Probe profile %q missing", uplink.ErrUnsupported, t.Ref)
		}
		if err := settledMark(c); err != nil {
			return err
		}
		if err := m.compatible(v, c); err != nil {
			return err
		}
		a, err := m.kernel()
		if err != nil {
			return err
		}
		return a.Probe(ctx, t, device)
	})
}

// PrepareForStart is called with the native operator's per-tunnel lock already
// held. It must not acquire the controller or try to take that lock again.
func (m *Managed) PrepareForStart(ctx context.Context, ref string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.profiles[ref]
	if !ok {
		return nil
	}
	if err := settledMark(c); err != nil {
		return err
	}
	v, err := m.deps.Lookup(ctx, ref)
	if err != nil {
		return err
	}
	if err = m.compatible(v, c); err != nil {
		return err
	}
	r, err := readBinding(c.BindingFile)
	if err != nil {
		return err
	}
	if err = m.ensure(ctx, c); err != nil {
		return err
	}
	a, err := m.kernel()
	if err != nil {
		return err
	}
	return a.activate(ctx, uplink.Target{Engine: "wireguard", Ref: ref}, r.Device, r.Device, true)
}
func (m *Managed) ReleaseUplink(ctx context.Context, t uplink.Target) error {
	return m.releaseUplink(ctx, t, false)
}

func (m *Managed) releaseUplink(ctx context.Context, t uplink.Target, orphanOnly bool) error {
	if !m.OwnsTarget(t.Ref) {
		return nil
	}
	return m.deps.WithLock(ctx, t.Ref, func() error {
		m.mu.Lock()
		defer m.mu.Unlock()
		c, ok := m.profiles[t.Ref]
		if !ok {
			return nil
		}
		v, err := m.target(ctx, t)
		orphan := false
		if err != nil {
			var checkErr error
			orphan, checkErr = m.confirmOrphan(ctx, t, c)
			if checkErr != nil {
				return fmt.Errorf("verify missing WireGuard target: %w", checkErr)
			}
			if !orphan {
				return err
			}
		} else {
			if orphanOnly {
				return uplink.ErrConflict
			}
			if err = m.compatible(v, c); err != nil {
				return err
			}
		}
		a, err := m.kernel()
		if err != nil {
			return err
		}
		s, err := a.auditResources(ctx, c, true, false)
		if err != nil {
			return err
		}
		if !orphan {
			r, err := readBinding(c.BindingFile)
			if err != nil {
				return err
			}
			p, err := m.provider(r.Device)
			if err != nil {
				return err
			}
			if err = m.deps.ApplyBinding(ctx, t.Ref, p.ID, r.Device); err != nil {
				return err
			}
		}
		for _, family := range []string{"-4", "-6"} {
			for _, retiredFamily := range s.RetiringRules {
				if retiredFamily == family {
					if _, err = m.deps.Run(ctx, "ip", family, "rule", "del", "pref", strconv.Itoa(c.Priority), "fwmark", fmt.Sprintf("0x%x", c.RetiringMark), "lookup", strconv.Itoa(c.Table)); err != nil {
						return err
					}
				}
			}
			missingRule := false
			for _, f := range s.MissingRules {
				missingRule = missingRule || f == family
			}
			if !missingRule {
				if _, err = m.deps.Run(ctx, "ip", family, "rule", "del", "pref", strconv.Itoa(c.Priority), "fwmark", fmt.Sprintf("0x%x", c.Mark), "lookup", strconv.Itoa(c.Table)); err != nil {
					return err
				}
			}
			out, e := m.deps.Run(ctx, "ip", family, "-j", "route", "show", "table", "all")
			if e != nil {
				return e
			}
			var routes []route
			if e = json.Unmarshal(out, &routes); e != nil {
				return e
			}
			for _, r := range routes {
				if fmt.Sprint(r.Table) != strconv.Itoa(c.Table) {
					continue
				}
				args := []string{family, "route", "del"}
				if r.Type == "unreachable" {
					args = append(args, "unreachable")
				}
				args = append(args, "default", "table", strconv.Itoa(c.Table), "proto", strconv.Itoa(routeProtocol), "metric", strconv.FormatUint(r.Metric, 10))
				if _, err = m.deps.Run(ctx, "ip", args...); err != nil {
					return err
				}
			}
		}
		delete(m.profiles, t.Ref)
		if err = m.save(); err != nil {
			m.profiles[t.Ref] = c
			return err
		}
		m.publishProtection()
		err = os.Remove(c.BindingFile)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	})
}

func (m *Managed) confirmOrphan(ctx context.Context, t uplink.Target, c Config) (bool, error) {
	if t.Engine != "wireguard" || t.Kind != "" || m.deps.ConfirmAbsent == nil {
		return false, nil
	}
	return m.deps.ConfirmAbsent(ctx, t.Ref, c.Interface)
}
