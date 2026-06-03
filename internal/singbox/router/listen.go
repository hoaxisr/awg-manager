package router

import (
	"fmt"
	"os"
	"strings"
)

// Socket states in /proc/net/{tcp,udp} (hex). TCP LISTEN = 0x0A; a bound,
// unconnected UDP socket = 0x07. sing-box's redirect-in (TCP) and tproxy-in
// (UDP) inbounds bind 0.0.0.0 in these states.
//
// Why state matters: sing-box's accepted TCP connections REUSE the listener's
// local port (RedirectPort), so /proc/net/tcp carries the LISTEN row plus one
// row per live flow — all sharing local port RedirectPort but in state 01
// (ESTABLISHED). Matching on the local port alone would false-positive on
// dozens of connections; we must also match the state.
const (
	tcpStateListen = "0A"
	udpStateBound  = "07"
)

// localPortInState reports whether procData (contents of /proc/net/tcp or
// /proc/net/udp) has a socket whose LOCAL-address port == port and whose
// state == state. It parses the fixed columns instead of substring-matching:
// column 1 (0-based) is local_address "HEXIP:HEXPORT", column 3 is the state.
func localPortInState(procData string, port int, state string) bool {
	want := fmt.Sprintf("%04X", port)
	for _, line := range strings.Split(procData, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		colon := strings.LastIndexByte(f[1], ':')
		if colon < 0 {
			continue
		}
		if f[1][colon+1:] == want && f[3] == state {
			return true
		}
	}
	return false
}

// singboxListeningProbe is the seam GetStatus uses to check sing-box socket
// binding. Overridable in tests so status checks don't touch real procfs.
var singboxListeningProbe = singboxIntercepting

// singboxIntercepting reports whether sing-box is actually listening on both
// router inbound sockets — the TCP REDIRECT port (LISTEN) and the UDP TPROXY
// port (bound). Process-alive (pidof) is not enough: an inbound that failed to
// bind would leave iptables handing packets to a dead socket. Reads procfs
// directly — ss/netstat are not in stock Entware. A read error reports false.
func singboxIntercepting() bool {
	tcp, err := os.ReadFile("/proc/net/tcp")
	if err != nil {
		return false
	}
	if !localPortInState(string(tcp), RedirectPort, tcpStateListen) {
		return false
	}
	udp, err := os.ReadFile("/proc/net/udp")
	if err != nil {
		return false
	}
	return localPortInState(string(udp), TPROXYPort, udpStateBound)
}
