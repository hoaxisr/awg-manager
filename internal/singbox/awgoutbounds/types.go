// internal/singbox/awgoutbounds/types.go
package awgoutbounds

import (
	"context"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/events"
	"github.com/hoaxisr/awg-manager/internal/singbox/orchestrator"
)

// TagInfo is the public projection of one AWG outbound that downstream
// consumers (deviceproxy selector members, router rule editor dropdown)
// use to show labels and store canonical tags.
type TagInfo struct {
	Tag   string `json:"tag"`   // "awg-{id}" or "awg-sys-{id}"
	Label string `json:"label"` // human-readable name
	Kind  string `json:"kind"`  // "managed" | "system"
	Iface string `json:"iface"` // kernel interface (t2s0, nwg0, etc.)
}

// AWGEntry is the internal representation produced by enumerate().
// Kept separate from TagInfo so internal evolution (e.g. adding state
// info, runtime hints) doesn't leak through the public API surface.
type AWGEntry struct {
	Tag   string
	Label string
	Kind  string
	Iface string
	// Resolver — готовый IP DNS-сервера туннеля; пусто = сервера нет
	// (системные NDMS-туннели DNS не несут), выше по коду подставляется
	// fallbackResolver.
	Resolver string
}

// Deps groups the external collaborators Service needs.
// ManagedServersQuery returns the InterfaceName of every awg-manager
// managed WireGuard server. enumerate() uses this list to skip those
// names in the system-tunnel branch — our servers must not become
// sing-box outbounds (clients connect TO them, traffic does not exit
// through them).
type ManagedServersQuery interface {
	ManagedServerInterfaceNames(ctx context.Context) []string
}

type Deps struct {
	AWGTunnels     AWGTunnelStore
	SystemTunnels  SystemTunnelQuery
	ManagedServers ManagedServersQuery // optional; nil = no filter applied
	Singbox        SingboxController   // nil ok — Sync skips both file write and Reload
	AppLog         AppLogger           // nil ok — degrades to silent
	Bus            *events.Bus         // nil ok — SubscribeBus becomes no-op
	// Orch is the config.d orchestrator. When non-nil (production),
	// writeFile hands the JSON to SlotAwg — orchestrator drives both
	// the atomic write and the debounced reload, so deps.Singbox.Reload
	// is skipped. When nil (tests), falls back to direct write +
	// Singbox.Reload.
	Orch *orchestrator.Orchestrator
}

// SingboxController is the narrow contract Service needs from
// singbox.Operator. Adapter pattern preserves the one-way dependency
// (singbox knows nothing about awgoutbounds).
type SingboxController interface {
	ConfigDir() string
	Reload() error
}

// AppLogger is the narrow logging contract — matches the project's
// existing logging.ScopedLogger surface (Info/Warn/Error).
type AppLogger interface {
	Info(action, target, message string)
	Warn(action, target, message string)
	Error(action, target, message string)
}

// ServiceImpl is the concrete Service. Constructor is in service.go.
type ServiceImpl struct {
	deps Deps
	mu   sync.Mutex

	// lastBytes is the marshalled payload of the previous successful
	// writeFile. Used to skip identical re-writes that would otherwise
	// arm a redundant sing-box reload (cascading from NDMS-hook driven
	// `tunnels` invalidations during NWG ping-check restart cycles).
	// nil before the first write; updated only on success. Guarded by mu.
	lastBytes []byte

	// sysClassNet allows tests to redirect kernel-iface presence checks
	// to a tempdir. Empty in production = "/sys/class/net".
	sysClassNet string
}
