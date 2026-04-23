package events

// EventType discriminates the 4 hook kinds we consume. Values match the
// NDMS hook script directory names (without the trailing .d), so the
// shared shell script can forward its invocation path as the type.
type EventType string

const (
	EventIfLayerChanged EventType = "iflayerchanged"
	EventIfCreated      EventType = "ifcreated"
	EventIfDestroyed    EventType = "ifdestroyed"
	EventIfIPChanged    EventType = "ifipchanged"
)

// Event is the unified hook payload. Fields that don't apply to a given
// EventType are left zero.
type Event struct {
	Type       EventType
	ID         string // NDMS interface id (e.g. "Wireguard0")
	SystemName string // kernel name (e.g. "nwg0"), if provided
	Layer      string // "conf" | "link" | "ipv4" | "ipv6" | "ctrl" (layerchanged only)
	Level      string // "running" | "disabled" | ... (layerchanged only)
	Address    string // IPv4 address (ipchanged only)
	Up         bool   // interface up flag (ipchanged; optional)
	Connected  bool   // interface connected flag (ipchanged; optional)
}
