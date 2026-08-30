// internal/singbox/awgoutbounds/service.go
package awgoutbounds

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
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
//
// When the orchestrator is wired, writeFile pushes through SlotAwg,
// which both writes the file and arms the debounced reload — calling
// Singbox.Reload here would just produce a redundant SIGHUP, so we
// skip it.
func (s *ServiceImpl) SyncAWGOutbounds(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	written, err := s.writeFile(ctx)
	if err != nil {
		return err
	}
	// Skip the reload when nothing changed — the orchestrator path debounces
	// internally, but the legacy direct-Reload path would otherwise SIGHUP
	// sing-box on every redundant tunnels-invalidation.
	if !written {
		return nil
	}
	if s.deps.Orch != nil {
		return nil
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
	_, err := s.writeFile(ctx)
	return err
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
			if ev.Type != events.EventResourceInvalidated {
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
// Returns (written, err): written=false means the marshalled payload was
// byte-identical to the previous successful write, so callers must skip
// downstream reloads too.
//
// The skip drops the 4–5 redundant SIGHUPs that NDMS-hook driven `tunnels`
// invalidations would otherwise cause during a single NWG ping-check restart
// cycle: the tunnel catalog is unchanged, so the file content is too.
func (s *ServiceImpl) writeFile(ctx context.Context) (bool, error) {
	entries, err := s.enumerate(ctx)
	if err != nil {
		s.logWarn("enumerate", "", err.Error())
		return false, err
	}
	data, mErr := marshalEntries(entries)
	if mErr != nil {
		s.logWarn("marshal", "15-awg.json", mErr.Error())
		return false, mErr
	}
	// First write always proceeds (lastBytes == nil) so the file is created
	// on boot even when the entry set is empty.
	if s.lastBytes != nil && bytes.Equal(data, s.lastBytes) {
		s.logInfo("sync", "15-awg.json", "skipped (unchanged)")
		return false, nil
	}
	if s.deps.Orch != nil {
		if err := s.deps.Orch.Save(orchestrator.SlotAwg, data); err != nil {
			s.logWarn("save", "15-awg.json", err.Error())
			return false, err
		}
		s.lastBytes = data
		s.logInfo("sync", "15-awg.json", fmt.Sprintf("%d outbounds written", len(entries)))
		return true, nil
	}
	if s.deps.Singbox == nil {
		// Without a Singbox controller we don't know the config dir;
		// skip the write rather than guess. Sync errors never block
		// the caller (per spec: "sync errors never block CRUD").
		return false, nil
	}
	path := filepath.Join(s.deps.Singbox.ConfigDir(), "15-awg.json")
	if err := saveFile(path, entries); err != nil {
		s.logWarn("save", path, err.Error())
		return false, err
	}
	s.lastBytes = data
	s.logInfo("sync", "15-awg.json", fmt.Sprintf("%d outbounds written", len(entries)))
	return true, nil
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
