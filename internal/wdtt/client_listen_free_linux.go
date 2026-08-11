//go:build linux

package wdtt

import (
	"github.com/hoaxisr/awg-manager/internal/childproc"
	"github.com/hoaxisr/awg-manager/internal/procport"
)

func freeStaleClientListenPort(clientBin, listen string) {
	if clientBin == "" {
		return
	}
	host, port, ok := procport.ParseListenHostPort(listen, "127.0.0.1")
	if !ok {
		return
	}
	info, err := procport.LookupListener(host, port, procport.ProtoUDP)
	if err != nil || !info.Open || info.PID <= 0 {
		return
	}
	if !childproc.MatchesBinary(info.PID, clientBin) {
		return
	}
	_, _ = procport.KillListener(host, port, procport.ProtoUDP)
}
