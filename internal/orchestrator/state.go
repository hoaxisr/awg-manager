package orchestrator

import (
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel"
	"github.com/hoaxisr/awg-manager/internal/tunnel/nwg"
)

// tunnelState is the orchestrator's view of a single tunnel.
type tunnelState struct {
	ID           string
	Name         string
	Backend      string // "kernel" | "nativewg"
	Enabled      bool
	Running      bool   // orchestrator's belief: tunnel is running
	Monitoring   bool   // monitor goroutine is active
	ActiveWAN    string
	NWGIndex     int
	PingCheck    *storage.TunnelPingCheck
	DefaultRoute bool
	ISPInterface string
	Endpoint             string    // peer endpoint (host:port)
	ExternalRestartCount int       // consecutive external restarts within window
	LastExternalRestart  time.Time // timestamp of last external restart
}

const (
	externalRestartMaxCount = 3
	externalRestartWindow   = 5 * time.Minute
)

func (t *tunnelState) canExternalRestart() bool {
	if time.Since(t.LastExternalRestart) > externalRestartWindow {
		return true
	}
	return t.ExternalRestartCount < externalRestartMaxCount
}

func (t *tunnelState) recordExternalRestart() {
	if time.Since(t.LastExternalRestart) > externalRestartWindow {
		t.ExternalRestartCount = 0
	}
	t.ExternalRestartCount++
	t.LastExternalRestart = time.Now()
}

// ndmsName returns the NDMS interface name for this tunnel.
func (t *tunnelState) ndmsName() string {
	if t.Backend == "nativewg" {
		return nwg.NewNWGNames(t.NWGIndex).NDMSName
	}
	return tunnel.NewNames(t.ID).NDMSName
}

// ifaceName returns the kernel interface name for this tunnel.
func (t *tunnelState) ifaceName() string {
	if t.Backend == "nativewg" {
		return nwg.NewNWGNames(t.NWGIndex).IfaceName
	}
	return tunnel.NewNames(t.ID).IfaceName
}

// State is the orchestrator's complete view of the system.
type State struct {
	tunnels     map[string]*tunnelState // tunnelID → state
	anyWANUpFn  func() bool            // delegates to wanModel.AnyUp()
	supportsASC bool
}

// newState creates an empty state.
func newState() State {
	return State{
		tunnels: make(map[string]*tunnelState),
	}
}

// findByNDMSName finds a tunnel by its NDMS interface name.
func (s *State) findByNDMSName(ndmsName string) *tunnelState {
	for _, t := range s.tunnels {
		if t.ndmsName() == ndmsName {
			return t
		}
	}
	return nil
}

// anyWANUp returns true if at least one WAN interface is up.
func (s *State) anyWANUp() bool {
	if s.anyWANUpFn != nil {
		return s.anyWANUpFn()
	}
	return false
}

// ensureTunnel loads a single tunnel into cache if not already present.
// Returns true if the tunnel exists (in cache or loaded from store).
func (s *State) ensureTunnel(tunnelID string, store *storage.AWGTunnelStore) bool {
	if _, ok := s.tunnels[tunnelID]; ok {
		return true
	}
	stored, err := store.Get(tunnelID)
	if err != nil {
		return false
	}
	s.tunnels[tunnelID] = tunnelStateFromStored(stored)
	return true
}

// tunnelStateFromStored creates a tunnelState from stored data.
func tunnelStateFromStored(t *storage.AWGTunnel) *tunnelState {
	return &tunnelState{
		ID:           t.ID,
		Name:         t.Name,
		Backend:      t.Backend,
		Enabled:      t.Enabled,
		NWGIndex:     t.NWGIndex,
		PingCheck:    t.PingCheck,
		DefaultRoute: t.DefaultRoute,
		ISPInterface: t.ISPInterface,
		Endpoint:     t.Peer.Endpoint,
		ActiveWAN:    t.ActiveWAN,
	}
}

// loadFromStore populates tunnel state from storage.
func (s *State) loadFromStore(store *storage.AWGTunnelStore) {
	tunnels, err := store.List()
	if err != nil {
		return
	}
	for _, t := range tunnels {
		s.tunnels[t.ID] = tunnelStateFromStored(&t)
	}
}
