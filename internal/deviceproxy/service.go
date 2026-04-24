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
	Store     *Store
	Tunnels   *storage.AWGTunnelStore // nil → treated as "no AWG tunnels"
	Singbox   SingboxOperator         // nil → treated as "no sb tunnels, no apply"
	NDMSQuery NDMSInterfaceQuery      // nil → ListenInterface resolution fails explicitly
	Bus       *events.Bus             // nil → no event subscriptions or publishes
}

// SingboxOperator is the narrow contract Service needs from
// singbox.Operator. The adapter in singbox_adapter.go (Task 11)
// implements this against the real Operator.
type SingboxOperator interface {
	ApplyDeviceProxy(ctx context.Context, spec ExternalSpec) error
	SetSelectorDefault(ctx context.Context, selectorTag, memberTag string) error
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

// ErrOutboundUnavailable is returned by SelectOutbound when the caller
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
		s.d.Bus.Publish("resource:invalidated", map[string]string{"kind": "deviceproxy"})
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

	// AWG targets
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

func (s *Service) listOutboundsLocked(_ context.Context) []Outbound {
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
	return out
}

// SelectOutbound switches the active member of the selector. Fast path:
// no reload. If sing-box is alive, the change is applied via Clash API;
// either way the choice is persisted so cold-start picks it up.
func (s *Service) SelectOutbound(ctx context.Context, tag string) error {
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

	cfg := s.d.Store.Get()
	cfg.SelectedOutbound = tag
	if err := s.d.Store.Save(cfg); err != nil {
		return fmt.Errorf("persist storage: %w", err)
	}

	if s.d.Singbox != nil && s.d.Singbox.IsRunning() {
		if err := s.d.Singbox.SetSelectorDefault(ctx, "device-proxy-selector", tag); err != nil {
			return fmt.Errorf("clash selector switch: %w", err)
		}
	}

	if s.d.Bus != nil {
		s.d.Bus.Publish("resource:invalidated", map[string]string{"kind": "deviceproxy"})
	}
	return nil
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
				s.d.Bus.Publish("resource:invalidated", map[string]string{"kind": "deviceproxy"})
			}
		}
	}

	// Rebuild sing-box config from whatever cfg is now.
	spec, err := s.buildSpec(ctx, cfg)
	if err != nil {
		return err
	}
	if s.d.Singbox != nil {
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
			switch ev.Type {
			case "tunnel:created", "tunnel:deleted", "singbox:tunnels-changed":
				if err := s.Reconcile(ctx); err != nil {
					// Reconcile failure is non-fatal at the subscriber level;
					// the user-facing flow already has its own error path.
					// No logger is wired on Service yet (would be added in a
					// future task); silent swallow matches the project's other
					// similar subscribers.
					_ = err
				}
			}
		}
	}()
	return unsub
}
