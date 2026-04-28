// internal/singbox/awgoutbounds/types.go
package awgoutbounds

import (
	"sync"

	"github.com/hoaxisr/awg-manager/internal/events"
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
// Same shape as TagInfo today; kept separate so internal evolution
// (e.g. adding state info, runtime hints) doesn't leak through the
// public API surface.
type AWGEntry struct {
	Tag   string
	Label string
	Kind  string
	Iface string
}

// Deps groups the external collaborators Service needs.
type Deps struct {
	AWGTunnels    AWGTunnelStore
	SystemTunnels SystemTunnelQuery
	Singbox       SingboxController // nil ok — Sync skips both file write and Reload
	AppLog        AppLogger         // nil ok — degrades to silent
	Bus           *events.Bus       // nil ok — SubscribeBus becomes no-op
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

	// sysClassNet allows tests to redirect kernel-iface presence checks
	// to a tempdir. Empty in production = "/sys/class/net".
	sysClassNet string
}
