package wdtt

import (
	"context"
	"time"
)

const (
	// clientRawRelayGrace — e2e probe для raw OpkgTun: operstate UP не гарантирует
	// рабочий relay после рестарта awg-manager (zombie wdtt-client без TUN fd).
	clientRawRelayGrace = 90 * time.Second
)

// RelayProbe performs a quick HTTP check through a kernel interface.
type RelayProbe interface {
	ProbeInterface(ctx context.Context, iface string) bool
}

// clientRawRelayUnhealthy: raw OpkgTun поднят, но трафик через интерфейс не
// проходит — типично после рестарта awg-manager на сервере (relay на той
// стороне сброшен, клиентский wdtt-client и opkgtun ещё «живы»).
//
// ctx — родитель супервизорного тика: при остановке демона/отмене тика проба
// обязана выйти вместе с ним, а не досиживать свои 8 с.
func clientRawRelayUnhealthy(ctx context.Context, cfg ClientConfig, probe RelayProbe, checker InterfaceChecker, st ProcessStatus, now time.Time) bool {
	if probe == nil || cfg.UsesWireGuard() || !cfg.usesNDMSOpkgTun() {
		return false
	}
	if !st.Running || st.StartedAt == nil {
		return false
	}
	if now.Sub(*st.StartedAt) < clientRawRelayGrace {
		return false
	}
	iface := cfg.kernelRawIface()
	if checker == nil || !checker.InterfaceOperUp(iface) {
		return false
	}
	probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	return !probe.ProbeInterface(probeCtx, iface)
}
