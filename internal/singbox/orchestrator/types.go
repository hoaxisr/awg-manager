// Package orchestrator owns the sing-box config.d/ directory: it is the
// single writer for per-domain JSON files, the only place that toggles
// a domain on/off (via rename-marker), and the only place that triggers
// a sing-box reload. Producers (tunnels, awg-outbounds, router,
// device-proxy) call Save/SetEnabled instead of touching files
// directly. This eliminates owner-confusion (one producer overwriting
// another's file) and divergence between Settings.X.Enabled and the
// actual merged config sing-box reads on start.
package orchestrator

import "time"

// Slot identifies a producer's well-known config block. The set is
// closed; new producers must be added to KnownSlots() and pick a
// non-conflicting filename prefix.
type Slot string

const (
	SlotBase          Slot = "base"          // 00-base.json — always on
	SlotTunnels       Slot = "tunnels"       // 10-tunnels.json
	SlotAwg           Slot = "awg"           // 15-awg.json
	SlotRouter        Slot = "router"        // 20-router.json
	SlotDeviceProxy   Slot = "deviceproxy"   // 30-deviceproxy.json
	SlotSubscriptions Slot = "subscriptions" // 40-subscriptions.json
)

// SlotMeta describes a producer's contract with the orchestrator.
// AlwaysOn slots cannot be disabled via SetEnabled.
//
// HasContent is consulted only for AlwaysOn slots and reports whether
// the slot has user-relevant content that justifies keeping the
// sing-box daemon running. AlwaysOn catalog slots (e.g. SlotAwg, which
// emits direct outbounds for use by other slots) leave it nil — they
// are infrastructure for consumers, not a reason to start the daemon
// on their own. Without this distinction a fresh install with no
// sing-box tunnels, no router, no device-proxy and no subscriptions
// would still keep sing-box running just to host an unused outbound
// catalog.
type SlotMeta struct {
	Slot       Slot
	Filename   string // bare filename, e.g. "20-router.json"
	AlwaysOn   bool
	HasContent func() bool
}

// SlotState is what Snapshot returns per registered slot.
type SlotState struct {
	Slot     Slot
	Filename string
	Enabled  bool // file lives in config.d/ (true) or config.d/disabled/ (false)
	Present  bool // file exists on disk in either location
	Bytes    int  // size of current JSON, 0 if absent
}

// KnownSlots returns the closed set of slots, in load order. tunnels
// and awg are AlwaysOn — their files always live in config.d/ so that
// CRUD by their producers (and merge by sing-box) is trivial. They do
// NOT activate the daemon on their own; tunnels gets a HasContent
// callback wired in main.go so it counts as "active work" only when
// the user has actually defined sing-box tunnels.
func KnownSlots() []SlotMeta {
	return []SlotMeta{
		{Slot: SlotBase, Filename: "00-base.json", AlwaysOn: true},
		{Slot: SlotTunnels, Filename: "10-tunnels.json", AlwaysOn: true},
		{Slot: SlotAwg, Filename: "15-awg.json", AlwaysOn: true},
		{Slot: SlotRouter, Filename: "20-router.json"},
		{Slot: SlotDeviceProxy, Filename: "30-deviceproxy.json"},
		{Slot: SlotSubscriptions, Filename: "40-subscriptions.json"},
	}
}

// DraftInfo is what DraftInfo() returns about a slot's pending file.
// HasDraft false means no pending file; in that case DraftedAt is zero.
type DraftInfo struct {
	HasDraft  bool
	DraftedAt time.Time
}

// reloadDebounce coalesces multiple Save/SetEnabled calls within this
// window into a single SIGHUP. 250ms is small enough to feel instant
// in UI flows, large enough to absorb chained internal mutations.
const reloadDebounce = 250 * time.Millisecond

// disabledSubdir is the (gitignored, sing-box-invisible) subdirectory
// where rename-markers park inactive slot files. Sing-box's -C is
// non-recursive so files here are not included in the merged config.
const disabledSubdir = "disabled"
