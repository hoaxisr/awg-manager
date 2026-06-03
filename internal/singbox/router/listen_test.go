package router

import "testing"

// Real /proc/net samples captured from a live router (sing-box running, router
// engine enabled). The TCP table carries the LISTEN row AND many ESTABLISHED
// rows that reuse local port C848 (51272) in state 01 — the decoys that a
// substring/port-only match would wrongly accept.
const (
	procTCPListenPlusEstablished = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
  33: 00000000:C848 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 81167401 1 ffffffc034ec6900 100 0 0 10 128
  71: 0100140A:C848 0300140A:EBB4 01 00000000:00000000 02:00006560 00000000     0        0 81251504 2 ffffffc034f32580 35 4 8 10 7
  76: 010A0A0A:C848 9E0A0A0A:D4B2 01 00000000:00000000 02:00003442 00000000     0        0 81215914 2 ffffffc03803c380 22 4 18 10 20
`
	procTCPEstablishedOnly = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
  71: 0100140A:C848 0300140A:EBB4 01 00000000:00000000 02:00006560 00000000     0        0 81251504 2 ffffffc034f32580 35 4 8 10 7
  76: 010A0A0A:C848 9E0A0A0A:D4B2 01 00000000:00000000 02:00003442 00000000     0        0 81215914 2 ffffffc03803c380 22 4 18 10 20
`
	procUDPBound = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops
   78: 00000000:C847 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 81167402 2 ffffffc024c44000 0
`
)

func TestLocalPortInState_TCPListen(t *testing.T) {
	if !localPortInState(procTCPListenPlusEstablished, RedirectPort, tcpStateListen) {
		t.Error("expected to find the LISTEN socket on RedirectPort amid established decoys")
	}
}

func TestLocalPortInState_IgnoresEstablishedDecoys(t *testing.T) {
	// Same local port, but only ESTABLISHED (st 01) rows — a port-only match
	// would false-positive here. State filtering must reject it.
	if localPortInState(procTCPEstablishedOnly, RedirectPort, tcpStateListen) {
		t.Error("must not treat ESTABLISHED connections (st 01) as a LISTEN socket")
	}
}

func TestLocalPortInState_UDPBound(t *testing.T) {
	if !localPortInState(procUDPBound, TPROXYPort, udpStateBound) {
		t.Error("expected the bound UDP TPROXY socket (st 07)")
	}
}

func TestLocalPortInState_WrongPort(t *testing.T) {
	if localPortInState(procUDPBound, RedirectPort, udpStateBound) {
		t.Error("must not match a different port")
	}
}
