//go:build linux

package wdtt

import (
	"github.com/hoaxisr/awg-manager/internal/childproc"
	"github.com/hoaxisr/awg-manager/internal/procport"
)

// freeStaleServerListenPorts stops leftover wdtt-server holding our UDP ports after a crash.
func freeStaleServerListenPorts(serverBin string, cfg ServerConfig) {
	if serverBin == "" {
		return
	}
	for _, addr := range cfg.ServerListenAddrs() {
		host, port, ok := procport.ParseListenHostPort(addr, "0.0.0.0")
		if !ok {
			continue
		}
		info, err := procport.LookupListener(host, port, procport.ProtoUDP)
		if err != nil || !info.Open || info.PID <= 0 {
			continue
		}
		if !childproc.MatchesBinary(info.PID, serverBin) {
			continue
		}
		_, _ = procport.KillListener(host, port, procport.ProtoUDP)
	}
}
