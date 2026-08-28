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
	Running      bool // orchestrator's belief: tunnel is running
	Monitoring   bool // monitor goroutine is active
	ActiveWAN    string
	NWGIndex     int
	PingCheck    *storage.TunnelPingCheck
	ISPInterface string
	// EndpointMayV6: peer endpoint — IPv6-литерал или hostname (может
	// резолвиться в v6). Для nativewg на ASC-прошивке это значит, что реальный
	// endpoint может жить только в ядре (wg set), а конфиг NDMS — нести
	// заглушку: после ребута нужен полный Start (decideBoot).
	EndpointMayV6 bool

	// ViaProxy: туннель идёт через awg_proxy.ko, а не через нативный ASC
	// прошивки. Так бывает и на ASC-прошивке: её ASC знает AmneziaWG только до
	// 2.0, а конфиг 3.0/3.1 обслуживает kmod (nwg.UsesProxyPath). От этого
	// зависит, поднимать ли туннель после ребута роутера и снимать ли слот при
	// падении WAN: NDMS сам умеет только свою половину, про слот он не знает.
	ViaProxy bool

	// quiescentUntil: while now < this, a conf=disabled edge for this tunnel
	// is treated as transient NDMS settling (do not stop). Set on (re)start.
	quiescentUntil time.Time

	// lastConfRunningAt: when an external conf=running edge was last seen for
	// this tunnel. settleConfDisabled uses it to tell an NDMS interface restart
	// (disabled→running bounce) from a real disable. Runtime-only.
	lastConfRunningAt time.Time
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
	anyWANUpFn  func() bool             // delegates to wanModel.AnyUp()
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
	// Тот же фильтр, что в loadFromStore, и по той же причине: зеркальная
	// запись прокси-выхода оркестратору не принадлежит. Второй путь загрузки
	// без этой проверки сводил бы первую на нет.
	if stored.Backend == "wdtt-raw" {
		return false
	}
	s.tunnels[tunnelID] = tunnelStateFromStored(stored)
	return true
}

// tunnelStateFromStored creates a tunnelState from stored data.
func tunnelStateFromStored(t *storage.AWGTunnel) *tunnelState {
	return &tunnelState{
		ID:            t.ID,
		Name:          t.Name,
		Backend:       t.Backend,
		Enabled:       t.Enabled,
		NWGIndex:      t.NWGIndex,
		PingCheck:     t.PingCheck,
		ISPInterface:  t.ISPInterface,
		ActiveWAN:     t.ActiveWAN,
		EndpointMayV6: nwg.EndpointMayResolveIPv6(t.Peer.Endpoint),
		ViaProxy:      t.Backend == "nativewg" && nwg.UsesProxyPath(&t.Interface),
	}
}

// loadFromStore populates tunnel state from storage.
func (s *State) loadFromStore(store *storage.AWGTunnelStore) {
	tunnels, err := store.List()
	if err != nil {
		return
	}
	for _, t := range tunnels {
		// Зеркальные записи raw-выходов — проекции прокси-инстансов, а не наши
		// туннели: их жизненным циклом целиком ведает прокси-рантайм. Сегодня
		// они безвредны лишь потому, что каждый switch по Backend перечисляет
		// бэкенды поимённо и не имеет ветки default — первая же такая ветка
		// начала бы стартовать и останавливать чужой ресурс. Не грузим вовсе.
		if t.Backend == "wdtt-raw" {
			continue
		}
		s.tunnels[t.ID] = tunnelStateFromStored(&t)
	}
}
