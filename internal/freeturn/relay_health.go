package freeturn

import (
	"context"
	"time"
)

const (
	// clientRelayGrace — короче DTLS-grace: e2e probe ловит «локальный WG жив,
	// relay до сервера мёртв» уже через ~1.5 мин после старта.
	clientRelayGrace = 90 * time.Second
)

// RelayProbe checks end-to-end data path through a linked tunnel interface.
type RelayProbe interface {
	ProbeInterface(ctx context.Context, iface string) bool
}

// LinkedTunnelResolver maps freeturn client id → linked AWG kernel iface.
type LinkedTunnelResolver interface {
	FreeTurnLinkedIface(clientID string) (iface string, ok bool)
}

// clientRelayUnhealthy: linked-туннель не проходит HTTP-check — типично после
// рестарта awg-manager на сервере: freeturn-client на клиенте жив, локальный WG
// handshake есть, но DTLS-реле до сервера мёртвое.
func clientRelayUnhealthy(probe RelayProbe, tunnels LinkedTunnelResolver, clientID string, st ProcessStatus, now time.Time) bool {
	if probe == nil || tunnels == nil {
		return false
	}
	if !st.Running || st.StartedAt == nil {
		return false
	}
	if now.Sub(*st.StartedAt) < clientRelayGrace {
		return false
	}
	iface, ok := tunnels.FreeTurnLinkedIface(clientID)
	if !ok || iface == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	return !probe.ProbeInterface(ctx, iface)
}
