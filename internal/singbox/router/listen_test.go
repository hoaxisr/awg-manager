package router

import (
	"encoding/binary"
	"fmt"
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

func TestAwgmProbeIgnoresRedirectPort(t *testing.T) {
	// В awgm-режиме redirect-inbound не создаётся: TCP и UDP приходят на один
	// tproxy-порт. Если probe продолжит требовать LISTEN на RedirectPort,
	// движок никогда не станет готовым и правила не установятся вовсе.
	// Формат строк — как парсит localPortInState: адрес:порт в hex, затем
	// состояние. TPROXYPort = 51271 = 0xC847.
	tcp := "  sl  local_address rem_address   st\n" +
		"   0: 0100007F:C847 00000000:0000 0A\n"
	udp := "  sl  local_address rem_address   st\n" +
		"   0: 0100007F:C847 00000000:0000 07\n"

	if !interceptingFromProcAwgm(tcp, udp) {
		t.Fatal("tproxy-порт слушается по TCP и UDP — probe обязан быть истинным")
	}

	// RedirectPort = 51272 = 0xC848 — в awgm-режиме к готовности отношения не имеет.
	tcpOnlyRedirect := "  sl  local_address rem_address   st\n" +
		"   0: 0100007F:C848 00000000:0000 0A\n"
	if interceptingFromProcAwgm(tcpOnlyRedirect, udp) {
		t.Fatal("LISTEN на RedirectPort в awgm-режиме не считается готовностью")
	}

	// Одного TCP LISTEN мало: dual-network inbound обязан держать и UDP-сокет,
	// иначе UDP-ветка TPROXY отдавала бы пакеты в никуда.
	if interceptingFromProcAwgm(tcp, "") {
		t.Fatal("без bound UDP-сокета на tproxy-порту готовности нет")
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

// procRouteHex renders a big-endian address the way the kernel prints it in
// /proc/net/route: %08X of the __be32, i.e. in host byte order.
func procRouteHex(addr [4]byte) string {
	return fmt.Sprintf("%08X", binary.NativeEndian.Uint32(addr[:]))
}

func TestParseProcRouteHex(t *testing.T) {
	// /proc/net/route stores 10.128.0.0 in host order: "0000800A" on
	// little-endian, "0A800000" on big-endian.
	got, ok := parseProcRouteHex(procRouteHex([4]byte{10, 128, 0, 0}))
	if !ok {
		t.Fatal("parse failed for valid 8-char hex")
	}
	if want := [4]byte{10, 128, 0, 0}; got != want {
		t.Fatalf("parseProcRouteHex(10.128.0.0) = %v, want %v", got, want)
	}
	// Mask /10 = 255.192.0.0.
	gotMask, ok := parseProcRouteHex(procRouteHex([4]byte{255, 192, 0, 0}))
	if !ok {
		t.Fatal("parse failed for mask hex")
	}
	if want := [4]byte{255, 192, 0, 0}; gotMask != want {
		t.Fatalf("parseProcRouteHex(255.192.0.0) = %v, want %v", gotMask, want)
	}
	if _, ok := parseProcRouteHex("short"); ok {
		t.Error("must reject non-8-char input")
	}
	if _, ok := parseProcRouteHex("ZZZZZZZZ"); ok {
		t.Error("must reject non-hex input")
	}
}
