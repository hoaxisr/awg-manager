package deviceproxy

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// Deps groups the external collaborators Service needs. Wired once at
// startup in main.go. Nil fields are tolerated — Service degrades and
// logs where applicable.
type Deps struct {
	Store         *Store
	Tunnels       *storage.AWGTunnelStore // nil → treated as "no AWG tunnels"
	SystemTunnels SystemTunnelQuery       // nil → system tunnels not included in outbound list
	Singbox       SingboxOperator         // nil → treated as "no sb tunnels, no apply"
	NDMSQuery     NDMSInterfaceQuery      // nil → ListenInterface resolution fails explicitly
	Bus           *events.Bus             // nil → no event subscriptions or publishes
}

// SingboxOperator is the narrow contract Service needs from
// singbox.Operator. Adapter in singbox_adapter.go binds it to the
// real Operator.
type SingboxOperator interface {
	ApplyDeviceProxy(ctx context.Context, spec ExternalSpec) error
	ApplyDeviceProxyNoReload(ctx context.Context, spec ExternalSpec) error
	SetSelectorDefault(ctx context.Context, selectorTag, memberTag string) error
	GetSelectorActive(ctx context.Context, selectorTag string) (string, error)
	TunnelTags() []string
	IsRunning() bool
}

// NDMSInterfaceQuery resolves an NDMS interface id (e.g. "Bridge0") to
// its current primary IPv4 address.
type NDMSInterfaceQuery interface {
	GetInterfaceAddress(ctx context.Context, ndmsID string) (string, error)
}

// ExternalSpec mirrors singbox.DeviceProxySpec but lives in this
// package to keep deviceproxy independent of singbox at the type
// level. The adapter translates.
type ExternalSpec struct {
	Enabled     bool
	ListenAddr  string
	Port        int
	Auth        AuthSpec
	SelectedTag string
	AWGTargets  []AWGTarget
	SBTags      []string
}

// AWGTarget is one AWG tunnel rendered into the sing-box config as a
// direct outbound with bind_interface.
type AWGTarget struct {
	TunnelID    string
	KernelIface string
}

// TunnelInboundPortsFn returns the set of listen_ports currently used
// by sing-box tunnel-internal inbounds. Used by ValidateConfig to
// detect port conflicts when the user picks a port for the device proxy.
type TunnelInboundPortsFn func() []int

// Service owns the deviceproxy storage + mutation surface. All public
// methods serialise through the embedded mutex.
type Service struct {
	d Deps

	mu          sync.Mutex
	tunnelPorts TunnelInboundPortsFn
}

// ErrOutboundUnavailable is returned by SelectRuntimeOutbound when the caller
// requests a tag that is not in the current list of available outbounds.
var ErrOutboundUnavailable = errors.New("outbound is not available")

func NewService(d Deps) *Service {
	return &Service{d: d}
}

// GetConfig returns the current persisted Config. Defensive copy via Store.
func (s *Service) GetConfig() Config {
	return s.d.Store.Get()
}

// SetTunnelInboundPorts wires a lookup that ValidateConfig uses to
// detect port conflicts with sing-box tunnel inbounds.
func (s *Service) SetTunnelInboundPorts(fn TunnelInboundPortsFn) {
	s.mu.Lock()
	s.tunnelPorts = fn
	s.mu.Unlock()
}

// withTunnelInboundPorts is a test helper that injects a fixed list.
func (s *Service) withTunnelInboundPorts(ports []int) {
	s.SetTunnelInboundPorts(func() []int { return ports })
}

// ValidateConfig checks the user-supplied Config for obvious errors
// before it is persisted. Errors wrap validation context so the API
// layer can surface them as 400 responses with meaningful messages.
func (s *Service) ValidateConfig(cfg Config) error {
	if !cfg.Enabled {
		return nil
	}
	if cfg.Port < 1024 || cfg.Port > 65535 {
		return fmt.Errorf("port %d is outside 1024-65535", cfg.Port)
	}
	s.mu.Lock()
	portFn := s.tunnelPorts
	s.mu.Unlock()
	if portFn != nil {
		for _, p := range portFn() {
			if p == cfg.Port {
				return fmt.Errorf("port %d is used by a sing-box tunnel inbound", cfg.Port)
			}
		}
	}
	if cfg.Auth.Enabled {
		if cfg.Auth.Username == "" {
			return fmt.Errorf("auth enabled but username is empty")
		}
		if cfg.Auth.Password == "" {
			return fmt.Errorf("auth enabled but password is empty")
		}
	}
	if !cfg.ListenAll && cfg.ListenInterface == "" {
		return fmt.Errorf("listen set to specific interface but interface is empty")
	}
	return nil
}

// SaveConfig validates, applies to sing-box, persists, and publishes.
// Transactional: on any failure nothing is persisted.
func (s *Service) SaveConfig(ctx context.Context, cfg Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.validateLocked(cfg); err != nil {
		return err
	}

	spec, err := s.buildSpec(ctx, cfg)
	if err != nil {
		return err
	}

	if s.d.Singbox != nil {
		if err := s.d.Singbox.ApplyDeviceProxy(ctx, spec); err != nil {
			return fmt.Errorf("apply to singbox: %w", err)
		}
	}

	if err := s.d.Store.Save(cfg); err != nil {
		return fmt.Errorf("persist storage: %w", err)
	}

	if s.d.Bus != nil {
		s.d.Bus.Publish("resource:invalidated", events.ResourceInvalidatedEvent{Resource: "deviceproxy"})
	}
	return nil
}

// validateLocked is the mutex-holding variant used by SaveConfig to
// avoid a nested Lock(). ValidateConfig (the public form) still works
// standalone for API-layer input checking.
func (s *Service) validateLocked(cfg Config) error {
	if !cfg.Enabled {
		return nil
	}
	if cfg.Port < 1024 || cfg.Port > 65535 {
		return fmt.Errorf("port %d is outside 1024-65535", cfg.Port)
	}
	if s.tunnelPorts != nil {
		for _, p := range s.tunnelPorts() {
			if p == cfg.Port {
				return fmt.Errorf("port %d is used by a sing-box tunnel inbound", cfg.Port)
			}
		}
	}
	if cfg.Auth.Enabled {
		if cfg.Auth.Username == "" {
			return fmt.Errorf("auth enabled but username is empty")
		}
		if cfg.Auth.Password == "" {
			return fmt.Errorf("auth enabled but password is empty")
		}
	}
	if !cfg.ListenAll && cfg.ListenInterface == "" {
		return fmt.Errorf("listen set to specific interface but interface is empty")
	}
	return nil
}

func (s *Service) buildSpec(ctx context.Context, cfg Config) (ExternalSpec, error) {
	spec := ExternalSpec{
		Enabled:     cfg.Enabled,
		Port:        cfg.Port,
		Auth:        cfg.Auth,
		SelectedTag: cfg.SelectedOutbound,
	}
	if cfg.ListenAll {
		spec.ListenAddr = "0.0.0.0"
	} else {
		if s.d.NDMSQuery == nil {
			return spec, fmt.Errorf("cannot resolve listen interface: NDMS query unavailable")
		}
		addr, err := s.d.NDMSQuery.GetInterfaceAddress(ctx, cfg.ListenInterface)
		if err != nil || addr == "" {
			return spec, fmt.Errorf("resolve listen interface %q: %w", cfg.ListenInterface, err)
		}
		spec.ListenAddr = addr
	}

	// AWG targets (managed tunnels)
	if s.d.Tunnels != nil {
		tunnels, _ := s.d.Tunnels.List()
		for _, t := range tunnels {
			t := t
			spec.AWGTargets = append(spec.AWGTargets, AWGTarget{
				TunnelID:    t.ID,
				KernelIface: awgKernelIface(&t),
			})
		}
	}

	// System tunnels (Keenetic native WireGuard, not managed by storage).
	// TunnelID is prefixed with "sys-" so the generated sing-box tag
	// becomes "awg-sys-<ID>" (e.g. "awg-sys-Wireguard0"), avoiding
	// collisions with managed AWG tunnel tags.
	if s.d.SystemTunnels != nil {
		sysTunnels, _ := s.d.SystemTunnels.List(ctx)
		for _, t := range sysTunnels {
			spec.AWGTargets = append(spec.AWGTargets, AWGTarget{
				TunnelID:    "sys-" + t.ID,
				KernelIface: t.InterfaceName,
			})
		}
	}

	// Sing-box tunnel tags
	if s.d.Singbox != nil {
		spec.SBTags = s.d.Singbox.TunnelTags()
	}
	return spec, nil
}

// awgKernelIface picks the kernel iface name for an AWG tunnel.
// NativeWG tunnels use nwg<Index>; legacy/kernel tunnels use the
// storage.ID directly (matches the convention in tunnel/ops).
func awgKernelIface(t *storage.AWGTunnel) string {
	if t.Backend == "nativewg" {
		return fmt.Sprintf("nwg%d", t.NWGIndex)
	}
	return t.ID
}

// RuntimeState is the UI-facing snapshot of the selector's live state.
// Not persisted; returned on demand.
type RuntimeState struct {
	Alive      bool   `json:"alive"`
	ActiveTag  string `json:"activeTag"`
	DefaultTag string `json:"defaultTag"`
}

// GetRuntimeState returns the current selector.now from Clash API
// (empty if sing-box is down) plus the persisted default for
// convenient client-side diffing.
func (s *Service) GetRuntimeState(ctx context.Context) RuntimeState {
	s.mu.Lock()
	defer s.mu.Unlock()

	state := RuntimeState{
		DefaultTag: s.d.Store.Get().SelectedOutbound,
	}
	if s.d.Singbox == nil || !s.d.Singbox.IsRunning() {
		return state
	}
	state.Alive = true
	if active, err := s.d.Singbox.GetSelectorActive(ctx, "device-proxy-selector"); err == nil {
		state.ActiveTag = active
	}
	return state
}

// Outbound describes one selectable proxy target exposed to the UI.
type Outbound struct {
	Tag    string `json:"tag"`
	Kind   string `json:"kind"`   // "direct" | "singbox" | "awg"
	Label  string `json:"label"`
	Detail string `json:"detail"` // extra info for UI (kernel iface, protocol, etc)
}

// ListOutbounds returns all members that can be assigned as the
// selector's active outbound — direct + every sb-tunnel tag + every
// AWG tunnel's awg-<id> tag. Order is deterministic: direct first,
// then sb by name, then AWG by id.
func (s *Service) ListOutbounds(ctx context.Context) []Outbound {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listOutboundsLocked(ctx)
}

func (s *Service) listOutboundsLocked(ctx context.Context) []Outbound {
	out := []Outbound{{Tag: "direct", Kind: "direct", Label: "Direct (WAN)", Detail: "без туннеля"}}

	if s.d.Singbox != nil {
		tags := append([]string(nil), s.d.Singbox.TunnelTags()...)
		sort.Strings(tags)
		for _, tag := range tags {
			out = append(out, Outbound{Tag: tag, Kind: "singbox", Label: tag})
		}
	}

	if s.d.Tunnels != nil {
		tunnels, _ := s.d.Tunnels.List()
		sort.Slice(tunnels, func(i, j int) bool { return tunnels[i].ID < tunnels[j].ID })
		for _, t := range tunnels {
			t := t
			iface := awgKernelIface(&t)
			out = append(out, Outbound{
				Tag:    "awg-" + t.ID,
				Kind:   "awg",
				Label:  t.Name,
				Detail: iface,
			})
		}
	}

	// System tunnels — Keenetic native WireGuard not managed by storage.
	// Tag uses the "awg-sys-" prefix to match what buildSpec/EnsureDeviceProxy
	// will generate for these entries.
	if s.d.SystemTunnels != nil {
		sysTunnels, _ := s.d.SystemTunnels.List(ctx)
		sort.Slice(sysTunnels, func(i, j int) bool { return sysTunnels[i].ID < sysTunnels[j].ID })
		for _, t := range sysTunnels {
			out = append(out, Outbound{
				Tag:    "awg-sys-" + t.ID,
				Kind:   "awg",
				Label:  t.Description,
				Detail: t.InterfaceName,
			})
		}
	}
	return out
}

// SelectRuntimeOutbound switches the live selector.now via Clash API.
// No storage write. No config.json write. The choice is ephemeral —
// sing-box reload or restart reverts to the persisted default.
//
// Errors:
//   - ErrOutboundUnavailable — tag is not in the currently-available list.
//   - singbox.ErrSingboxNotRunning — bubbled up from the operator when
//     the daemon is down, so API layer can map to 409.
func (s *Service) SelectRuntimeOutbound(ctx context.Context, tag string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	available := s.listOutboundsLocked(ctx)
	found := false
	for _, ob := range available {
		if ob.Tag == tag {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%w: %q", ErrOutboundUnavailable, tag)
	}

	if s.d.Singbox == nil {
		return fmt.Errorf("singbox operator unavailable")
	}
	return s.d.Singbox.SetSelectorDefault(ctx, "device-proxy-selector", tag)
}

// Reconcile is the single idempotent rebuild path. It verifies the
// currently-selected outbound still exists in the available list
// (disables the proxy + publishes deviceproxy:missing-target if not)
// and re-applies the resulting spec to sing-box.
func (s *Service) Reconcile(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg := s.d.Store.Get()
	if cfg.Enabled && cfg.SelectedOutbound != "" {
		available := s.listOutboundsLocked(ctx)
		found := false
		for _, ob := range available {
			if ob.Tag == cfg.SelectedOutbound {
				found = true
				break
			}
		}
		if !found {
			wasTag := cfg.SelectedOutbound
			cfg.Enabled = false
			cfg.SelectedOutbound = ""
			if err := s.d.Store.Save(cfg); err != nil {
				return fmt.Errorf("persist after missing target: %w", err)
			}
			if s.d.Bus != nil {
				s.d.Bus.Publish("deviceproxy:missing-target", map[string]string{"wasTag": wasTag})
				s.d.Bus.Publish("resource:invalidated", events.ResourceInvalidatedEvent{Resource: "deviceproxy"})
			}
		}
	}

	// Rebuild sing-box config from whatever cfg is now.
	spec, err := s.buildSpec(ctx, cfg)
	if err != nil {
		return err
	}
	if s.d.Singbox != nil {
		// Skip the apply if there is nothing meaningful to do — no proxy,
		// no sing-box tunnels, no AWG tunnels. Applying in this case would
		// just write an empty config.json + start sing-box for nothing.
		if !spec.Enabled && len(spec.SBTags) == 0 && len(spec.AWGTargets) == 0 {
			return nil
		}
		if err := s.d.Singbox.ApplyDeviceProxy(ctx, spec); err != nil {
			return fmt.Errorf("apply spec: %w", err)
		}
	}
	return nil
}

// BridgeChoice describes a single Bridge interface for the inbound
// listen address dropdown.
type BridgeChoice struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	IP    string `json:"ip"`
}

// ListenChoicesResult aggregates the data the UI needs to render the
// inbound settings form.
type ListenChoicesResult struct {
	LanIP          string         `json:"lanIP"`
	Bridges        []BridgeChoice `json:"bridges"`
	SingboxRunning bool           `json:"singboxRunning"`
}

// bridgeLister is the optional interface NDMSAdapter implements so that
// ListenChoices can enumerate Bridge interfaces. Guarded by a type
// assertion so the rest of NDMSInterfaceQuery is unchanged.
type bridgeLister interface {
	ListBridges(ctx context.Context) ([]BridgeChoice, error)
}

// ListenChoices returns the bridge list, LAN IP, and singbox-running
// status needed by the frontend inbound settings form.
func (s *Service) ListenChoices(ctx context.Context) (ListenChoicesResult, error) {
	res := ListenChoicesResult{Bridges: []BridgeChoice{}}
	if s.d.Singbox != nil {
		res.SingboxRunning = s.d.Singbox.IsRunning()
	}
	if lister, ok := s.d.NDMSQuery.(bridgeLister); ok {
		bridges, err := lister.ListBridges(ctx)
		if err == nil {
			res.Bridges = bridges
			for _, b := range bridges {
				if b.ID == "Bridge0" && b.IP != "" {
					res.LanIP = b.IP
					break
				}
			}
			if res.LanIP == "" {
				for _, b := range bridges {
					if b.IP != "" {
						res.LanIP = b.IP
						break
					}
				}
			}
		}
	}
	return res, nil
}

// SubscribeBus registers event handlers that trigger Reconcile. Call
// once at startup. Returns an unsubscribe function to call during
// shutdown.
func (s *Service) SubscribeBus(ctx context.Context) func() {
	if s.d.Bus == nil {
		return func() {}
	}
	_, ch, unsub := s.d.Bus.Subscribe()
	go func() {
		for ev := range ch {
			if ev.Type != "resource:invalidated" && ev.Type != "singbox:tunnels-changed" {
				continue
			}
			if ev.Type == "resource:invalidated" {
				// Only react to invalidations that change our child list.
				payload, ok := ev.Data.(events.ResourceInvalidatedEvent)
				if !ok {
					continue
				}
				if payload.Resource != "tunnels" && payload.Resource != "singbox.tunnels" {
					continue
				}
			}
			if err := s.Reconcile(ctx); err != nil {
				// Reconcile failure is non-fatal at the subscriber level;
				// the user-facing flow already has its own error path.
				// No logger is wired on Service yet (would be added in a
				// future task); silent swallow matches the project's other
				// similar subscribers.
				_ = err
			}
		}
	}()
	return unsub
}
