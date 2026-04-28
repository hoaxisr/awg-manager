package deviceproxy

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/events"
)

// Deps groups the external collaborators Service needs. Wired once at
// startup in main.go. Nil fields are tolerated — Service degrades and
// logs where applicable.
type Deps struct {
	Store        *Store
	Singbox      SingboxOperator     // nil → treated as "no sb tunnels, no apply"
	NDMSQuery    NDMSInterfaceQuery  // nil → ListenInterface resolution fails explicitly
	Bus          *events.Bus         // nil → no event subscriptions or publishes
	AWGOutbounds AWGOutboundsCatalog // nil → AWG-related selector members empty
}

// AWGOutboundsCatalog is the narrow contract Service needs from the
// awgoutbounds package. Defined here (not imported) to keep the
// dependency direction clean — main.go injects the real impl.
type AWGOutboundsCatalog interface {
	ListTags(ctx context.Context) ([]AWGTagInfo, error)
}

// AWGTagInfo is deviceproxy's projection of awgoutbounds.TagInfo.
// Same shape; lives here so deviceproxy doesn't depend on the
// awgoutbounds package types.
type AWGTagInfo struct {
	Tag   string
	Label string
	Kind  string
	Iface string
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
	AWGTags     []string
	SBTags      []string
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

// validateConfigRaw contains the stateless validation rules that both
// ValidateConfig (public, takes the mutex) and validateLocked
// (internal, caller holds the mutex) share.
func validateConfigRaw(cfg Config, tunnelPorts TunnelInboundPortsFn) error {
	if !cfg.Enabled {
		return nil
	}
	if cfg.Port < 1024 || cfg.Port > 65535 {
		return fmt.Errorf("port %d is outside 1024-65535", cfg.Port)
	}
	if tunnelPorts != nil {
		for _, p := range tunnelPorts() {
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

// ValidateConfig checks the user-supplied Config for obvious errors
// before it is persisted. Errors wrap validation context so the API
// layer can surface them as 400 responses with meaningful messages.
func (s *Service) ValidateConfig(cfg Config) error {
	s.mu.Lock()
	portFn := s.tunnelPorts
	s.mu.Unlock()
	return validateConfigRaw(cfg, portFn)
}

// SaveConfig validates, applies to sing-box, and persists cfg.
// Transactional on the pre-apply phase; post-apply errors are logged
// but do not roll back persisted storage.
//
// Reload decision: if the diff between old and new is a SelectedOutbound-
// only change (and both states are Enabled, and sing-box is running), the
// process is NOT reloaded — we surgically rewrite config.json so the new
// selector.default takes effect on next reload/restart, and the current
// live selector.now (possibly set by a hot-switch) stays untouched.
// Any other change (port, listen, auth, enabled toggle) requires a
// full reload. When sing-box is not running the full apply path is always
// taken so the cold-start safety net (startAndWait) fires normally.
func (s *Service) SaveConfig(ctx context.Context, cfg Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.validateLocked(cfg); err != nil {
		return err
	}

	oldCfg := s.d.Store.Get()

	spec, err := s.buildSpec(ctx, cfg)
	if err != nil {
		return err
	}

	if s.d.Singbox != nil {
		// No-reload path only makes sense when the daemon is actually up —
		// otherwise there's no live selector.now to preserve, AND the
		// reload path includes a cold-start safety net that ApplyConfigNoReload
		// deliberately skips. Require both conditions.
		if onlySelectedOutboundChanged(oldCfg, cfg) && s.d.Singbox.IsRunning() {
			if err := s.d.Singbox.ApplyDeviceProxyNoReload(ctx, spec); err != nil {
				return fmt.Errorf("apply to singbox (no-reload): %w", err)
			}
		} else {
			if err := s.d.Singbox.ApplyDeviceProxy(ctx, spec); err != nil {
				return fmt.Errorf("apply to singbox: %w", err)
			}
		}
	}

	if err := s.d.Store.Save(cfg); err != nil {
		return fmt.Errorf("persist storage: %w", err)
	}

	if s.d.Bus != nil {
		s.d.Bus.Publish("resource:invalidated", events.ResourceInvalidatedEvent{Resource: "deviceproxy.config"})
		// A default-only change also shifts what the runtime store would
		// derive "temporarily" against, so invalidate both.
		s.d.Bus.Publish("resource:invalidated", events.ResourceInvalidatedEvent{Resource: "deviceproxy.runtime"})
	}
	return nil
}

// ForceApply re-applies the currently-persisted Config to sing-box via
// the full reload path, regardless of whether anything changed since
// last Save. Used by the "Применить сейчас" UI action when the user
// has saved a new default via the no-reload surgical path and wants
// the live selector.now to snap to that default immediately — the
// reload causes sing-box to reinit selector.now from the updated
// config.json's selector.default.
//
// Client SOCKS connections are interrupted during reload; that trade-off
// is explicit since the user clicked a reload button.
func (s *Service) ForceApply(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	cfg := s.d.Store.Get()

	spec, err := s.buildSpec(ctx, cfg)
	if err != nil {
		return err
	}

	if s.d.Singbox != nil {
		if err := s.d.Singbox.ApplyDeviceProxy(ctx, spec); err != nil {
			return fmt.Errorf("force apply: %w", err)
		}
	}

	if s.d.Bus != nil {
		s.d.Bus.Publish("resource:invalidated", events.ResourceInvalidatedEvent{Resource: "deviceproxy.config"})
		s.d.Bus.Publish("resource:invalidated", events.ResourceInvalidatedEvent{Resource: "deviceproxy.runtime"})
	}
	return nil
}

// onlySelectedOutboundChanged returns true when the only field that
// differs between old and new is SelectedOutbound AND both states are
// Enabled. Used by SaveConfig to decide whether to skip the sing-box
// reload (live selector.now must be preserved through the save).
func onlySelectedOutboundChanged(oldCfg, newCfg Config) bool {
	if !oldCfg.Enabled || !newCfg.Enabled {
		return false
	}
	copyNew := newCfg
	copyNew.SelectedOutbound = oldCfg.SelectedOutbound
	return copyNew == oldCfg
}

// validateLocked is the mutex-holding variant used by SaveConfig to
// avoid a nested Lock(). ValidateConfig (the public form) still works
// standalone for API-layer input checking.
func (s *Service) validateLocked(cfg Config) error {
	return validateConfigRaw(cfg, s.tunnelPorts)
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

	// AWG tags — single source of truth is the awgoutbounds package,
	// which enumerates managed + system tunnels and emits canonical
	// awg-{id} / awg-sys-{id} tags. We just collect the tags.
	if s.d.AWGOutbounds != nil {
		tags, err := s.d.AWGOutbounds.ListTags(ctx)
		if err == nil {
			for _, t := range tags {
				spec.AWGTags = append(spec.AWGTags, t.Tag)
			}
		}
	}

	// Sing-box tunnel tags
	if s.d.Singbox != nil {
		spec.SBTags = s.d.Singbox.TunnelTags()
	}
	return spec, nil
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
	defaultTag := s.d.Store.Get().SelectedOutbound
	sb := s.d.Singbox
	s.mu.Unlock()

	state := RuntimeState{DefaultTag: defaultTag}
	if sb == nil || !sb.IsRunning() {
		return state
	}
	state.Alive = true
	if active, err := sb.GetSelectorActive(ctx, "device-proxy-selector"); err == nil {
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

	if s.d.AWGOutbounds != nil {
		tags, err := s.d.AWGOutbounds.ListTags(ctx)
		if err == nil {
			for _, t := range tags {
				out = append(out, Outbound{
					Tag:    t.Tag,
					Kind:   "awg",
					Label:  t.Label,
					Detail: t.Iface,
				})
			}
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
	available := s.listOutboundsLocked(ctx)
	sb := s.d.Singbox
	bus := s.d.Bus
	s.mu.Unlock()

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

	if sb == nil {
		return fmt.Errorf("singbox operator unavailable")
	}
	if err := sb.SetSelectorDefault(ctx, "device-proxy-selector", tag); err != nil {
		return err
	}

	// Hot-switch changed runtime.activeTag — give the frontend SSE
	// fast-path so the "Активный туннель" card updates sub-second,
	// without waiting for the 5s runtime polling tick.
	if bus != nil {
		bus.Publish("resource:invalidated", events.ResourceInvalidatedEvent{Resource: "deviceproxy.runtime"})
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
				s.d.Bus.Publish("resource:invalidated", events.ResourceInvalidatedEvent{Resource: "deviceproxy.config"})
				s.d.Bus.Publish("resource:invalidated", events.ResourceInvalidatedEvent{Resource: "deviceproxy.runtime"})
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
		if !spec.Enabled && len(spec.SBTags) == 0 && len(spec.AWGTags) == 0 {
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

// HasSelectorReference reports whether the persisted Config references
// the given outbound tag as the user-chosen SelectedOutbound default.
// Used by tunnel.Service.Delete to refuse deletions that would orphan
// the user's explicit choice.
//
// Selector membership (the dynamic awg-* member list rebuilt every
// buildSpec call) is NOT consulted: every existing AWG tunnel is in
// that list by construction, so consulting it would refuse every
// delete. Membership disappears naturally on the next Reconcile after
// the tunnel is gone, and the awgoutbounds + deviceproxy reload chain
// is debounce-coalesced so sing-box never sees an inconsistent state.
func (s *Service) HasSelectorReference(tag string) bool {
	s.mu.Lock()
	cfg := s.d.Store.Get()
	s.mu.Unlock()
	return cfg.SelectedOutbound == tag
}
