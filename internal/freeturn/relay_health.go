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
//
// Тот же linked-туннель параллельно мониторит pingcheck своим независимым
// контуром; наш рестарт касается только ft-client (не туннеля), а затухание
// повторов на обеих сторонах — через backoff (см. supervisor.go), полного
// дедупа контуров нет.
//
// ctx — родитель супервизорного тика: при остановке демона/отмене тика проба
// обязана выйти вместе с ним, а не досиживать свои 8 с.
func clientRelayUnhealthy(ctx context.Context, probe RelayProbe, tunnels LinkedTunnelResolver, clientID string, st ProcessStatus, now time.Time) bool {
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
	probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	return !probe.ProbeInterface(probeCtx, iface)
}
