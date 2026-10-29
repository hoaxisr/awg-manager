package router

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
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

func stubSysClassNet(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := sysClassNetDir
	sysClassNetDir = dir
	t.Cleanup(func() { sysClassNetDir = old })
	return dir
}

func writeCarrier(t *testing.T, root, iface, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, iface), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, iface, "carrier"), []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
}

// carrier=1 — единственное «готов»; 0, отсутствие файла и пустое имя — «не готов».
// Раньше оба теста не доходили до сравнения с "1": `return false` был зелёным.
func TestTunInterfaceReady_CarrierDecides(t *testing.T) {
	root := stubSysClassNet(t)
	writeCarrier(t, root, "opkgtun3", "1\n")
	writeCarrier(t, root, "opkgtun4", "0\n")

	if !tunInterfaceReady("opkgtun3") {
		t.Error("carrier=1 → ready")
	}
	if tunInterfaceReady("opkgtun4") {
		t.Error("carrier=0 → not ready")
	}
	if tunInterfaceReady("opkgtun5") {
		t.Error("нет устройства → not ready")
	}
	if tunInterfaceReady("") {
		t.Error("пустое имя → not ready")
	}
}

func TestSysClassNetDirDefault(t *testing.T) {
	if sysClassNetDir != "/sys/class/net" {
		t.Fatalf("дефолт шва = %q", sysClassNetDir)
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

// Разбор маршрутов tun (F756), с клоном кэша на ядрах до 4.15: ошибка здесь — снятие нужного маршрута на
// каждом тике, поэтому проверяется на живом формате обеих таблиц.
func TestParseTunRoutes(t *testing.T) {
	v4 := "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n" +
		"opkgtun0\t" + procRouteHex([4]byte{57, 144, 0, 0}) + "\t00000000\t0001\t0\t0\t0\t" + procRouteHex([4]byte{255, 252, 0, 0}) + "\t0\t0\t0\n" +
		"opkgtun0\t" + procRouteHex([4]byte{198, 18, 0, 0}) + "\t00000000\t0001\t0\t0\t0\t" + procRouteHex([4]byte{255, 254, 0, 0}) + "\t0\t0\t0\n" +
		"eth3\t" + procRouteHex([4]byte{8, 8, 8, 0}) + "\t00000000\t0001\t0\t0\t0\t" + procRouteHex([4]byte{255, 255, 255, 0}) + "\t0\t0\t0\n"
	v6 := "2a032880000000000000000000000000 20 00000000000000000000000000000000 00 00000000000000000000000000000000 00000400 00000001 00000000 00000001 opkgtun0\n" +
		"2a032880000000000000000000000000 20 00000000000000000000000000000000 00 00000000000000000000000000000000 00000400 00000001 00000000 00000001 opkgtun0\n" +
		// клон кэша (RTF_CACHE) — не маршрут, в список не идёт
		"2a032880f00a00080face00b00c00002 80 00000000000000000000000000000000 00 00000000000000000000000000000000 00000000 00000001 00000003 01000001 opkgtun0\n" +
		"fe800000000000000000000000000000 40 00000000000000000000000000000000 00 00000000000000000000000000000000 00000100 00000001 00000000 00000001 eth3\n"
	got := parseTunRoutes("opkgtun0", v4, v6)
	want := []netip.Prefix{
		netip.MustParsePrefix("57.144.0.0/14"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("2a03:2880::/32"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseTunRoutes = %v, want %v", got, want)
	}
}

// Ядро без IPv6 (на Keenetic компонент отключаемый): /proc/net/ipv6_route нет —
// v4-маршруты всё равно читаются, иначе снос и пробы молча выключились бы.
func TestLiveFakeIPTunRoutes_NoIPv6Table(t *testing.T) {
	dir := t.TempDir()
	v4 := filepath.Join(dir, "route")
	data := "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n" +
		"opkgtun0\t" + procRouteHex([4]byte{57, 144, 0, 0}) + "\t00000000\t0001\t0\t0\t0\t" + procRouteHex([4]byte{255, 252, 0, 0}) + "\t0\t0\t0\n"
	if err := os.WriteFile(v4, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	old4, old6 := procNetRoute, procNetIPv6Route
	procNetRoute, procNetIPv6Route = v4, filepath.Join(dir, "absent")
	t.Cleanup(func() { procNetRoute, procNetIPv6Route = old4, old6 })

	got, err := liveFakeIPTunRoutes("opkgtun0")
	if err != nil {
		t.Fatalf("нет ipv6_route — не ошибка: %v", err)
	}
	if want := []netip.Prefix{netip.MustParsePrefix("57.144.0.0/14")}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
