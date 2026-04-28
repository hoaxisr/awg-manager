// internal/singbox/awgoutbounds/service.go
package awgoutbounds

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/hoaxisr/awg-manager/internal/events"
)

// Service is the public contract used by tunnel.Service (as AWGSyncer
// target), deviceproxy (for selector members), and router (for rule
// outbound picker).
type Service interface {
	// SyncAWGOutbounds enumerates the catalog, writes 15-awg.json,
	// and triggers Operator.Reload(). Idempotent.
	SyncAWGOutbounds(ctx context.Context) error

	// Reconcile is SyncAWGOutbounds without the Reload — used at boot
	// before sing-box is started.
	Reconcile(ctx context.Context) error

	// ListTags returns the current tag set with metadata for UI consumers.
	// Source of truth is the live catalog (not the file), so callers see
	// fresh CRUD state immediately, not after the reload cycle.
	ListTags(ctx context.Context) ([]TagInfo, error)
}

// NewService constructs the Service. All Deps fields are optional —
// nil triggers safe degradation (logged warnings are emitted via the
// app log, wired separately in main.go).
func NewService(d Deps) *ServiceImpl {
	return &ServiceImpl{deps: d}
}

// Compile-time guarantee that ServiceImpl satisfies Service.
var _ Service = (*ServiceImpl)(nil)

// SyncAWGOutbounds writes 15-awg.json and triggers a sing-box reload.
func (s *ServiceImpl) SyncAWGOutbounds(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writeFile(ctx); err != nil {
		return err
	}
	if s.deps.Singbox != nil {
		return s.deps.Singbox.Reload()
	}
	return nil
}

// Reconcile is the boot-safe variant: writes the file but does NOT
// reload. Used by main.go before Operator.Start.
func (s *ServiceImpl) Reconcile(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeFile(ctx)
}

// SubscribeBus listens for events that change the AWG-tunnel set
// and triggers SyncAWGOutbounds. Specifically: resource:invalidated
// for "tunnels" (managed AWG CRUD) and "system-tunnels" (NDMS hooks
// firing when a Keenetic-native WireGuard interface is added/removed
// out-of-band from awg-manager). Without this, deleting a system
// tunnel via NDMS UI would leave a stale awg-sys-{id} entry in
// 15-awg.json with a now-missing bind_interface.
//
// Returns an unsubscribe function. Safe to call once at boot.
func (s *ServiceImpl) SubscribeBus(ctx context.Context) func() {
	if s.deps.Bus == nil {
		return func() {}
	}
	_, ch, unsub := s.deps.Bus.Subscribe()
	go func() {
		for ev := range ch {
			if ev.Type != "resource:invalidated" {
				continue
			}
			payload, ok := ev.Data.(events.ResourceInvalidatedEvent)
			if !ok {
				continue
			}
			// React only to events that change which tunnels exist.
			switch payload.Resource {
			case "tunnels", "singbox.tunnels", "system-tunnels":
			default:
				continue
			}
			if err := s.SyncAWGOutbounds(ctx); err != nil {
				// Sync failures are non-fatal at the subscriber level;
				// the writeFile path already logs via AppLog.
				_ = err
			}
		}
	}()
	return unsub
}

// ListTags exposes the current set of AWG tags with their human labels.
// Built from a fresh enumerate(), so deviceproxy/router see the
// post-CRUD state without waiting for the reload cycle.
func (s *ServiceImpl) ListTags(ctx context.Context) ([]TagInfo, error) {
	entries, err := s.enumerate(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]TagInfo, 0, len(entries))
	for _, e := range entries {
		out = append(out, TagInfo{
			Tag: e.Tag, Label: e.Label, Kind: e.Kind, Iface: e.Iface,
		})
	}
	return out, nil
}

// writeFile is the shared body used by Sync + Reconcile. Caller holds mu.
func (s *ServiceImpl) writeFile(ctx context.Context) error {
	entries, err := s.enumerate(ctx)
	if err != nil {
		s.logWarn("enumerate", "", err.Error())
		return err
	}
	if s.deps.Singbox == nil {
		// Without a Singbox controller we don't know the config dir;
		// skip the write rather than guess. Sync errors never block
		// the caller (per spec: "sync errors never block CRUD").
		return nil
	}
	path := filepath.Join(s.deps.Singbox.ConfigDir(), "15-awg.json")
	if err := saveFile(path, entries); err != nil {
		s.logWarn("save", path, err.Error())
		return err
	}
	s.logInfo("sync", "15-awg.json", fmt.Sprintf("%d outbounds written", len(entries)))
	return nil
}

func (s *ServiceImpl) logInfo(action, target, msg string) {
	if s.deps.AppLog != nil {
		s.deps.AppLog.Info(action, target, msg)
	}
}

func (s *ServiceImpl) logWarn(action, target, msg string) {
	if s.deps.AppLog != nil {
		s.deps.AppLog.Warn(action, target, msg)
	}
}
