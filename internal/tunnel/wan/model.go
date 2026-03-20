// Package wan provides a unified WAN interface state model.
// Single source of truth for WAN state: populated once at boot from NDMS,
// updated in real-time by iflayerchanged hooks.
package wan

import (
	"sort"
	"sync"
)

// NOTE: Subsumption (hiding physical WANs when higher-level protocols run over them)
// was intentionally removed. The ipv4=running hook already reports which interface
// has real IPv4 connectivity. If ISP doesn't get IPv4 under PPPoE, it won't be Up.

// Interface represents a single WAN connection.
type Interface struct {
	Name     string // Kernel name: "ppp0", "eth3" — PRIMARY KEY in the model
	ID       string // NDMS ID: "PPPoE0", "ISP" — used for RCI calls
	Label    string // Human-readable: "Letai" or generated from type
	Up       bool   // IPv4 connectivity (from hooks or boot query)
	Priority int    // NDMS priority (higher = preferred by user)
}

// InterfaceStatus is the per-interface status returned by Status().
type InterfaceStatus struct {
	Up       bool   `json:"up"`
	Label    string `json:"label"`
	Priority int    `json:"priority"`
}

// Model is the unified WAN state model.
// All WAN state lives here: interface up/down tracking.
type Model struct {
	mu           sync.RWMutex
	interfaces   map[string]*Interface
	populated    bool
	repopulateFn func() // called when SetUp encounters unknown interface
}

// NewModel creates a new empty WAN state model.
func NewModel() *Model {
	return &Model{
		interfaces: make(map[string]*Interface),
	}
}

// Populate fills the model from NDMS data at boot. Called once.
// Subsequent calls overwrite the model (e.g. for re-initialization).
func (m *Model) Populate(interfaces []Interface) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.interfaces = make(map[string]*Interface, len(interfaces))
	for _, iface := range interfaces {
		i := iface
		m.interfaces[i.Name] = &i
	}
	m.populated = true
}

// SetRepopulateFn sets the callback invoked when SetUp encounters an unknown
// interface (e.g. USB modem hotplugged, new PPPoE configured after boot).
// The callback should re-query NDMS and call Populate.
func (m *Model) SetRepopulateFn(fn func()) {
	m.repopulateFn = fn
}

// SetUp updates the up/down state of a WAN interface.
// Called by hook handler on ipv4 state change.
// If the interface is unknown and the model has been populated, triggers
// a full re-populate from NDMS to discover newly configured WANs.
func (m *Model) SetUp(name string, up bool) {
	m.mu.Lock()
	if iface, ok := m.interfaces[name]; ok {
		iface.Up = up
		m.mu.Unlock()
		return
	}
	populated := m.populated
	m.mu.Unlock()

	// Unknown interface after initial populate — re-query NDMS
	if populated && m.repopulateFn != nil {
		m.repopulateFn()
		// Apply hook state after repopulate (NDMS snapshot may lag behind hook)
		m.mu.Lock()
		if iface, ok := m.interfaces[name]; ok {
			iface.Up = up
		}
		m.mu.Unlock()
	}
}

// IsUp returns true if the named interface is up.
func (m *Model) IsUp(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if iface, ok := m.interfaces[name]; ok {
		return iface.Up
	}
	return false
}

// Known returns true if the interface is tracked by the WAN model.
// Non-WAN interfaces (bridges, loopback, etc.) are not tracked.
func (m *Model) Known(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.interfaces[name]
	return ok
}

// AnyUp returns true if at least one WAN interface is up.
// Used for diagnostics and API status only — lifecycle uses IsUp per-tunnel.
func (m *Model) AnyUp() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, iface := range m.interfaces {
		if iface.Up {
			return true
		}
	}
	return false
}

// PreferredUp returns the name of the Up interface with the highest priority.
// Returns ("", false) if no interfaces are up or the model is empty.
func (m *Model) PreferredUp() (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var best *Interface
	for _, iface := range m.interfaces {
		if !iface.Up {
			continue
		}
		if best == nil || iface.Priority > best.Priority {
			best = iface
		}
	}
	if best == nil {
		return "", false
	}
	return best.Name, true
}

// ForUI returns all WAN interfaces visible to the user, sorted by name.
func (m *Model) ForUI() []Interface {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]Interface, 0, len(m.interfaces))
	for _, iface := range m.interfaces {
		result = append(result, *iface)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

// Status returns the full state for the /wan/status API endpoint.
func (m *Model) Status() map[string]InterfaceStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]InterfaceStatus, len(m.interfaces))
	for name, iface := range m.interfaces {
		result[name] = InterfaceStatus{
			Up:       iface.Up,
			Label:    iface.Label,
			Priority: iface.Priority,
		}
	}
	return result
}

// GetLabel returns the human-readable label for a WAN interface by name.
// Returns empty string if the interface is not found.
func (m *Model) GetLabel(name string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if iface, ok := m.interfaces[name]; ok {
		return iface.Label
	}
	return ""
}

// IDFor returns the NDMS ID for the given kernel name (reverse lookup).
// Returns empty string if the interface is not found.
func (m *Model) IDFor(kernelName string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if iface, ok := m.interfaces[kernelName]; ok {
		return iface.ID
	}
	return ""
}

// NameForID returns the kernel name for the given NDMS ID (reverse lookup).
// Returns empty string if no interface with this ID exists.
func (m *Model) NameForID(ndmsID string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, iface := range m.interfaces {
		if iface.ID == ndmsID {
			return iface.Name
		}
	}
	return ""
}

// IsPopulated returns true after Populate() has been called.
func (m *Model) IsPopulated() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.populated
}


