package router

import (
	"net/netip"
	"testing"
)

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

// ---------------------------------------------------------------------------
// fakeip-tun readiness probes
// ---------------------------------------------------------------------------

func TestTunInterfaceReady_NonexistentIface(t *testing.T) {
	// A name that cannot exist under /sys/class/net → read error → not ready.
	if tunInterfaceReady("opkgtun-nonexistent-xyz") {
		t.Error("nonexistent tun iface must report not-ready")
	}
}

func TestTunInterfaceReady_EmptyIface(t *testing.T) {
	if tunInterfaceReady("") {
		t.Error("empty iface name must report not-ready")
	}
}

func TestFakeIPDNSProbe_RejectsEmptyOrInvalid(t *testing.T) {
	if liveFakeIPDNSProbe(t.Context(), "", netip.MustParsePrefix("10.128.0.0/10")) {
		t.Error("empty dnsAddr must fail-closed")
	}
	if liveFakeIPDNSProbe(t.Context(), "172.18.0.2", netip.Prefix{}) {
		t.Error("invalid fakeip prefix must fail-closed")
	}
}

func TestFakeIPPoolRoutePresent_RejectsInvalidInputs(t *testing.T) {
	if liveFakeIPPoolRoutePresent("", netip.MustParsePrefix("10.128.0.0/10")) {
		t.Error("empty iface must fail-closed")
	}
	if liveFakeIPPoolRoutePresent("opkgtun0", netip.Prefix{}) {
		t.Error("invalid pool must fail-closed")
	}
	if liveFakeIPPoolRoutePresent("opkgtun0", netip.MustParsePrefix("3f80::/10")) {
		t.Error("v6 pool must fail-closed (v4-only for v1)")
	}
}

func TestParseProcRouteHex(t *testing.T) {
	// /proc/net/route stores 10.128.0.0 as little-endian "0000800A".
	got, ok := parseProcRouteHex("0000800A")
	if !ok {
		t.Fatal("parse failed for valid 8-char hex")
	}
	if want := [4]byte{10, 128, 0, 0}; got != want {
		t.Fatalf("parseProcRouteHex(0000800A) = %v, want %v", got, want)
	}
	// Mask /10 = 255.192.0.0 stored little-endian as "0000C0FF".
	gotMask, ok := parseProcRouteHex("0000C0FF")
	if !ok {
		t.Fatal("parse failed for mask hex")
	}
	if want := [4]byte{255, 192, 0, 0}; gotMask != want {
		t.Fatalf("parseProcRouteHex(0000C0FF) = %v, want %v", gotMask, want)
	}
	if _, ok := parseProcRouteHex("short"); ok {
		t.Error("must reject non-8-char input")
	}
	if _, ok := parseProcRouteHex("ZZZZZZZZ"); ok {
		t.Error("must reject non-hex input")
	}
}
