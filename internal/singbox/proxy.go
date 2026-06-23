package singbox

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/command"
	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/sys/ndmsinfo"
)

// markProxyMgrDur — ВРЕМЕННЫЙ helper для perf-diagnostics. Логирует через
// slog.Default — в этом пакете нет ScopedLogger в ProxyManager. Удалить
// после perf-сессии 2026-05-23.
func markProxyMgrDur(label string, start time.Time) {
	slog.Info("perf-proxy", "label", label, "ms", time.Since(start).Milliseconds())
}

// maxProxySlots caps how many ProxyN slots we will scan when looking for
// a free index. Keenetic does not publish an official ceiling; 128 is
// well above any realistic tunnel count and bounds the loop in NextFreeIndex
// in case NDMS ever returns something unexpected.
const maxProxySlots = 128

// ErrProxyComponentMissing is returned when the router lacks the NDMS
// "proxy" component. Without it, no ProxyN interface can be created, so
// sing-box cannot route any traffic. Surfaced to the UI as a distinct
// state (separate from generic RCI errors) so we can show the user how
// to fix it instead of a raw NDMS error string.
var ErrProxyComponentMissing = fmt.Errorf("NDMS 'proxy' component is not installed — sing-box integration unavailable")

// ProxyManager orchestrates NDMS Proxy interfaces for sing-box tunnels.
// Reads go through queries.Interfaces (GetProxy helper); writes through
// commands.Proxies.
type ProxyManager struct {
	queries  *query.Queries
	commands *command.Commands
}

func NewProxyManager(q *query.Queries, c *command.Commands) *ProxyManager {
	return &ProxyManager{queries: q, commands: c}
}

// EnsureProxy creates or refreshes ProxyN pointing at 127.0.0.1:port.
// Idempotent: re-creating with same params is safe. Returns
// ErrProxyComponentMissing before talking to NDMS when the required
// component is absent.
func (pm *ProxyManager) EnsureProxy(ctx context.Context, index, port int, description string) error {
	defer markProxyMgrDur(fmt.Sprintf("EnsureProxy(%d)", index), time.Now())
	if !ndmsinfo.HasProxyComponent() {
		return ErrProxyComponentMissing
	}
	name := fmt.Sprintf("%s%d", proxyIfacePrefix, index)
	return pm.commands.Proxies.CreateProxy(ctx, name, description, "127.0.0.1", port, true)
}

// NextFreeIndex returns the lowest ProxyN index not occupied on the
// router. The NDMS namespace is shared with whatever the user created
// manually through the router UI, so we must scan /show/interface/
// before picking a slot — otherwise CreateProxy would silently mutate
// the user's existing Proxy0. reserved lets a batch allocator skip
// indices it has already handed out earlier in the same batch, before
// those ProxyN interfaces have been committed to NDMS.
func (pm *ProxyManager) NextFreeIndex(ctx context.Context, reserved map[int]bool) (int, error) {
	defer markProxyMgrDur("NextFreeIndex", time.Now())
	ifaces, err := pm.queries.Interfaces.List(ctx)
	if err != nil {
		return 0, fmt.Errorf("list interfaces: %w", err)
	}
	used := make(map[int]bool)
	for idx := range reserved {
		used[idx] = true
	}
	for _, iface := range ifaces {
		if !strings.HasPrefix(iface.ID, proxyIfacePrefix) {
			continue
		}
		var idx int
		if n, err := fmt.Sscanf(iface.ID, proxyIfacePrefix+"%d", &idx); err != nil || n != 1 {
			continue
		}
		used[idx] = true
	}
	for i := 0; i < maxProxySlots; i++ {
		if !used[i] {
			return i, nil
		}
	}
	return 0, fmt.Errorf("no free Proxy slot (scanned %d)", maxProxySlots)
}

// RemoveProxy tears down ProxyN.
func (pm *ProxyManager) RemoveProxy(ctx context.Context, index int) error {
	defer markProxyMgrDur(fmt.Sprintf("RemoveProxy(%d)", index), time.Now())
	name := fmt.Sprintf("%s%d", proxyIfacePrefix, index)
	_ = pm.commands.Proxies.ProxyDown(ctx, name) // ignore error — may be already down
	return pm.commands.Proxies.DeleteProxy(ctx, name)
}

// RemoveOrphanSingboxProxies удаляет ProxyN, ассоциированные с sing-box,
// которые остались в NDMS после перехода в режим "NDMS Proxy disabled"
// (или после кривого middle-of-MigrateOff обрыва). Безопасно сохраняет
// Proxy, созданные пользователем вручную.
//
// Критерии "ours":
//  1. iface.Description совпадает с одним из tunnelTags (наш ProxyManager
//     пишет tunnel tag в description — proxy.go:47, operator.go:1292).
//  2. iface.Description пустой И idx попадает в ourPortSlots (некоторые
//     версии прошивки могут не возвращать description в List).
//
// Прочие ProxyN остаются нетронутыми — это пользовательские интерфейсы.
// Best-effort: при ошибке удаления одного proxy переходит к следующему,
// возвращает первую ошибку.
func (pm *ProxyManager) RemoveOrphanSingboxProxies(ctx context.Context, tunnelTags map[string]bool, ourPortSlots, subProxyIdx map[int]bool) error {
	ifaces, err := pm.queries.Interfaces.List(ctx)
	if err != nil {
		return fmt.Errorf("list interfaces: %w", err)
	}
	var firstErr error
	for _, iface := range ifaces {
		if !strings.HasPrefix(iface.ID, proxyIfacePrefix) {
			continue
		}
		var idx int
		if n, e := fmt.Sscanf(iface.ID, proxyIfacePrefix+"%d", &idx); e != nil || n != 1 {
			continue
		}
		if !proxyIsOurs(idx, iface.Description, tunnelTags, ourPortSlots, subProxyIdx) {
			continue
		}
		if err := pm.RemoveProxy(ctx, idx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// ListNativeProxies returns kernel names (e.g. "t2s0") of NDMS Proxy
// interfaces NOT created by us — KeenOS-native SOCKS proxies the user may
// bind a router direct outbound to (#323). Mirrors RemoveOrphanSingboxProxies'
// enumeration but inverts the ownership test and resolves kernel names.
func (pm *ProxyManager) ListNativeProxies(ctx context.Context, tunnelTags map[string]bool, ourPortSlots, subProxyIdx map[int]bool) ([]string, error) {
	ifaces, err := pm.queries.Interfaces.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list interfaces: %w", err)
	}
	var entries []proxyEntry
	for _, iface := range ifaces {
		if !strings.HasPrefix(iface.ID, proxyIfacePrefix) {
			continue
		}
		var idx int
		if n, e := fmt.Sscanf(iface.ID, proxyIfacePrefix+"%d", &idx); e != nil || n != 1 {
			continue
		}
		kernel := pm.queries.Interfaces.ResolveSystemName(ctx, iface.ID)
		if kernel == "" {
			continue
		}
		entries = append(entries, proxyEntry{idx: idx, desc: iface.Description, kernel: kernel})
	}
	return nativeProxyKernelNames(entries, tunnelTags, ourPortSlots, subProxyIdx), nil
}

// SubscriptionProxy describes an NDMS ProxyN created for a subscription
// composite (urltest/selector). These live in a separate managed set from
// tunnel proxies (Tunnels()): their port and proxy index are allocated by the
// subscription system, not derived from listen_port-firstPort.
type SubscriptionProxy struct {
	Index int
	Port  int
	Label string
}

// SubscriptionProxySet enumerates active subscription composite proxies.
// Implemented by the wiring layer over the subscription store.
type SubscriptionProxySet interface {
	SubscriptionProxies() []SubscriptionProxy
}

// proxyIsOurs reports whether ProxyN (index idx, interface description desc) was
// created by awg-manager for sing-box. Tunnel proxies are matched by their tag
// description or port slot; subscription composites carry the user label as
// description (not a tunnel tag), so they are recognised by their explicitly
// tracked proxy index instead.
func proxyIsOurs(idx int, desc string, tunnelTags map[string]bool, ourPortSlots, subProxyIdx map[int]bool) bool {
	if subProxyIdx[idx] {
		return true
	}
	if desc != "" {
		return tunnelTags[desc]
	}
	return ourPortSlots[idx]
}

// proxyEntry is an NDMS Proxy interface candidate: NDMS slot index, NDMS
// description, and resolved kernel name (e.g. "t2s0").
type proxyEntry struct {
	idx    int
	desc   string
	kernel string
}

// nativeProxyKernelNames returns kernel names of Proxy interfaces NOT created
// by us — KeenOS-native SOCKS proxies the user may bind a router direct
// outbound to (#323). Pure filter over proxyIsOurs; I/O lives in the caller.
func nativeProxyKernelNames(proxies []proxyEntry, tunnelTags map[string]bool, ourPortSlots, subProxyIdx map[int]bool) []string {
	var out []string
	for _, p := range proxies {
		if proxyIsOurs(p.idx, p.desc, tunnelTags, ourPortSlots, subProxyIdx) {
			continue
		}
		out = append(out, p.kernel)
	}
	return out
}

// SyncProxies reconciles NDMS Proxy interfaces with current config.json tunnels.
// Creates missing Proxy for each tunnel and brings existing Proxy up if Down.
// Removal of proxies for absent tunnels is the Operator's responsibility.
func (pm *ProxyManager) SyncProxies(ctx context.Context, tunnels []TunnelInfo) error {
	for _, t := range tunnels {
		var idx int
		if _, err := fmt.Sscanf(t.ProxyInterface, proxyIfacePrefix+"%d", &idx); err != nil {
			return fmt.Errorf("bad proxy iface name %q: %w", t.ProxyInterface, err)
		}
		info, err := pm.queries.Interfaces.GetProxy(ctx, t.ProxyInterface)
		if err != nil || !info.Exists {
			if err := pm.EnsureProxy(ctx, idx, t.ListenPort, t.Tag); err != nil {
				return err
			}
			continue
		}
		if !info.Up {
			if err := pm.commands.Proxies.ProxyUp(ctx, t.ProxyInterface); err != nil {
				return err
			}
		}
	}
	return nil
}
