#!/usr/bin/env python3
"""Патчи SpaceNeuroX monolith (server.go) + копирование server_direct/server_raw для Keenetic."""
from __future__ import annotations

import pathlib
import re
import shutil
import sys

ROOT = pathlib.Path(sys.argv[1] if len(sys.argv) > 1 else ".")
PATCH_DIR = pathlib.Path(__file__).resolve().parent
SRC_RAW = PATCH_DIR.parent / "server_raw.go"


def is_v14_monolith(text: str) -> bool:
    """SpaceNeuroX v1.4+ ships listen-raw/direct and rawRouter in server.go."""
    return 'listenRaw := flag.String("listen-raw"' in text


def patch_server_go(text: str) -> str:
    if is_v14_monolith(text):
        return patch_server_go_v14(text)
    return patch_server_go_legacy(text)


def patch_server_go_v14(text: str) -> str:
    if "var wgIfaceName" not in text:
        text = re.sub(
            r"\nconst \(\n\twgIfaceName\s+=\s+\"wdtt0\"\n",
            '\nvar wgIfaceName = "wdtt0"\n\nconst (\n',
            text,
            count=1,
        )

    if "flagNoNAT" not in text:
        text = text.replace(
            '\tdnsFlag := flag.String("dns", "8.8.8.8", "DNS серверы для клиентов")\n\tflag.Parse()',
            '\tdnsFlag := flag.String("dns", "8.8.8.8", "DNS серверы для клиентов")\n'
            '\tflagNoNAT := flag.Bool("no-nat", false, "skip iptables/nft NAT (awg-manager на роутере)")\n'
            '\tflagWGIface := flag.String("wg-iface", "", "userspace WG iface (opkgtunN для Keenetic)")\n'
            '\tflagNatIface := flag.String("nat-if", "", "egress interface for MASQUERADE")\n'
            '\tflag.Parse()',
        )
        text = text.replace(
            "\tdns = *dnsFlag\n\n\tlog.SetFlags",
            "\tdns = *dnsFlag\n"
            "\tkeeneticNoNAT = *flagNoNAT\n"
            "\tkeeneticNatIface = strings.TrimSpace(*flagNatIface)\n"
            "\tif n := strings.TrimSpace(*flagWGIface); n != \"\" {\n"
            "\t\twgIfaceName = n\n"
            "\t}\n\n"
            "\tlog.SetFlags",
        )

    if "keeneticNoNAT" not in text.split("setupFullConeNAT", 1)[1][:400]:
        text = text.replace(
            "func setupFullConeNAT(wgIface string) error {\n\tlog.Println(\"[NAT]",
            "func setupFullConeNAT(wgIface string) error {\n\tif keeneticNoNAT {\n"
            "\t\tlog.Println(\"[NAT] пропуск (-no-nat)\")\n"
            "\t\tnatType = \"disabled (-no-nat)\"\n"
            "\t\treturn nil\n"
            "\t}\n\tlog.Println(\"[NAT]",
            1,
        )

    nat_ext = (
        "\textIface := getDefaultInterface()\n"
        "\tlog.Printf(\"[NAT] Внешний: %s\", extIface)"
    )
    nat_ext_new = (
        "\textIface := keeneticNatIface\n"
        "\tif extIface == \"\" {\n"
        "\t\textIface = getDefaultInterface()\n"
        "\t}\n"
        "\tlog.Printf(\"[NAT] Внешний: %s\", extIface)"
    )
    if nat_ext in text:
        text = text.replace(nat_ext, nat_ext_new, 1)

    raw_nat = "func setupRawNAT(rawIface string) error {\n\textIface := getDefaultInterface()"
    raw_nat_new = (
        "func setupRawNAT(rawIface string) error {\n"
        "\tif keeneticNoNAT {\n"
        "\t\tlog.Println(\"[RAW-NAT] пропуск (-no-nat)\")\n"
        "\t\tsetupForwardRules(rawIface)\n"
        "\t\treturn nil\n"
        "\t}\n"
        "\textIface := keeneticNatIface\n"
        "\tif extIface == \"\" {\n"
        "\t\textIface = getDefaultInterface()\n"
        "\t}"
    )
    if raw_nat in text:
        text = text.replace(raw_nat, raw_nat_new, 1)

    text = patch_stats_active_devices(text)
    text = patch_dtls_idle_timeout(text)
    text = patch_get_next_ip_skip_gateway(text)
    text = patch_pacer_and_downlink(text)
    return text


def patch_pacer_and_downlink(text: str) -> str:
    # 1. Remove artificial rate throttling from pacer, increase worker buffer from 256 to 1024
    old_pacer_const = (
        "const downlinkWorkerBuf = 256\n\n"
        "// rawDownlinkRate ограничивает одну TURN-аллокацию немного ниже наблюдаемого\n"
        "// VK лимита ~260 KiB/s. Пейсер сглаживает bursts, не меняя протокол клиента.\n"
        "const (\n"
        "\trawDownlinkRate  = 247 * 1024\n"
        "\trawDownlinkBurst = 16 * 1024\n"
        ")"
    )
    new_pacer_const = (
        "const downlinkWorkerBuf = 1024\n\n"
        "// rawDownlinkRate: 0 отключает искусственное ограничение скорости для максимального throughput\n"
        "const (\n"
        "\trawDownlinkRate  = 0\n"
        "\trawDownlinkBurst = 0\n"
        ")"
    )
    if old_pacer_const in text:
        text = text.replace(old_pacer_const, new_pacer_const, 1)

    # 2. Add multi-worker failover to dispatchDownlink
    old_downlink_loop = (
        "\t\tw := r.pickDownlinkConn(dst, len(pkt))\n"
        "\t\tif w == nil {\n"
        "\t\t\tif atomic.CompareAndSwapUint32(&r.noSessionLogged, 0, 1) {\n"
        "\t\t\t\tlog.Printf(\"[RAW] downlink: нет сессии для %s (пакет от интернета, но клиент не зарегистрирован)\", dst)\n"
        "\t\t\t}\n"
        "\t\t\tcontinue\n"
        "\t\t}\n"
        "\t\tif atomic.CompareAndSwapUint32(&r.firstDownlink, 0, 1) {\n"
        "\t\t\tlog.Printf(\"[RAW] Первый downlink-пакет доставлен клиенту %s (%d байт)\", dst, len(pkt))\n"
        "\t\t}\n"
        "\t\t// Копируем в pooled-буфер: pkt живёт в общем buf, который readLoop\n"
        "\t\t// тут же перезапишет следующим Read — writer-горутина воркера должна\n"
        "\t\t// получить свою независимую копию, раз запись теперь асинхронная.\n"
        "\t\tout := getBuf2048()[:len(pkt)]\n"
        "\t\tcopy(out, pkt)\n"
        "\t\tw.enqueue(out)"
    )
    new_downlink_loop = (
        "\t\tout := getBuf2048()[:len(pkt)]\n"
        "\t\tcopy(out, pkt)\n"
        "\t\tif !r.dispatchDownlink(dst, out) {\n"
        "\t\t\tputBuf2048(out)\n"
        "\t\t}"
    )
    if old_downlink_loop in text:
        text = text.replace(old_downlink_loop, new_downlink_loop, 1)

    old_pick = (
        "// pickDownlinkConn выбирает воркера для очередного downlink-пакета клиента\n"
        "// dst, размазывая нагрузку по всем его зарегистрированным воркерам\n"
        "// адаптивными чанками (см. downlinkChunkSizeFor) с предохранителем\n"
        "// downlinkMaxDwellMS на случай, если текущий relay начал тормозить.\n"
        "func (r *rawRouter) pickDownlinkConn(dst string, pktSize int) *downlinkWorker {\n"
    )
    new_dispatch = (
        "// dispatchDownlink отправляет downlink-пакет клиенту dst, распределяя по воркерам\n"
        "// и выполняя failover на свободные воркеры при временной занятости очереди.\n"
        "func (r *rawRouter) dispatchDownlink(dst string, pkt []byte) bool {\n"
        "\tr.mu.Lock()\n"
        "\tcs := r.sessions[dst]\n"
        "\tif cs == nil || len(cs.workers) == 0 {\n"
        "\t\tr.mu.Unlock()\n"
        "\t\tif atomic.CompareAndSwapUint32(&r.noSessionLogged, 0, 1) {\n"
        "\t\t\tlog.Printf(\"[RAW] downlink: нет сессии для %s (пакет от интернета, но клиент не зарегистрирован)\", dst)\n"
        "\t\t}\n"
        "\t\treturn false\n"
        "\t}\n"
        "\tn := len(cs.workers)\n"
        "\tif cs.rrIndex >= n {\n"
        "\t\tcs.rrIndex = 0\n"
        "\t}\n"
        "\tnow := time.Now().UnixMilli()\n"
        "\tif cs.chunkStartTs == 0 {\n"
        "\t\tcs.chunkStartTs = now\n"
        "\t} else if now-cs.chunkStartTs >= downlinkMaxDwellMS {\n"
        "\t\tcs.rrIndex = (cs.rrIndex + 1) % n\n"
        "\t\tcs.rrCount = 0\n"
        "\t\tcs.chunkStartTs = now\n"
        "\t}\n"
        "\tstart := cs.rrIndex\n"
        "\tcs.rrCount++\n"
        "\tif cs.rrCount >= downlinkChunkSizeFor(len(pkt)) {\n"
        "\t\tcs.rrIndex = (cs.rrIndex + 1) % n\n"
        "\t\tcs.rrCount = 0\n"
        "\t\tcs.chunkStartTs = now\n"
        "\t}\n"
        "\tif atomic.CompareAndSwapUint32(&r.firstDownlink, 0, 1) {\n"
        "\t\tlog.Printf(\"[RAW] Первый downlink-пакет доставлен клиенту %s (%d байт)\", dst, len(pkt))\n"
        "\t}\n"
        "\tfor i := 0; i < n; i++ {\n"
        "\t\tw := cs.workers[(start+i)%n]\n"
        "\t\tif w != nil {\n"
        "\t\t\tselect {\n"
        "\t\t\tcase w.sendCh <- pkt:\n"
        "\t\t\t\tr.mu.Unlock()\n"
        "\t\t\t\treturn true\n"
        "\t\t\tdefault:\n"
        "\t\t\t}\n"
        "\t\t}\n"
        "\t}\n"
        "\tr.mu.Unlock()\n"
        "\treturn false\n"
        "}\n\n"
        "func (r *rawRouter) pickDownlinkConn(dst string, pktSize int) *downlinkWorker {\n"
    )
    if old_pick in text:
        text = text.replace(old_pick, new_dispatch, 1)

    return text


def patch_server_go_legacy(text: str) -> str:
    if "flagListenDirect" not in text:
        text = re.sub(
            r"\nconst \(\n\twgIfaceName\s+=\s+\"wdtt0\"\n",
            '\nvar wgIfaceName = "wdtt0"\n\nconst (\n',
            text,
            count=1,
        )

        text = text.replace(
            '\tdnsFlag := flag.String("dns", "8.8.8.8", "DNS серверы для клиентов")\n\tflag.Parse()',
            '\tdnsFlag := flag.String("dns", "8.8.8.8", "DNS серверы для клиентов")\n'
            '\tflagListenDirect := flag.String("listen-direct", "", "WRAP UDP без DTLS (WG, быстрее DTLS)")\n'
            '\tflagListenRaw := flag.String("listen-raw", "", "Raw UDP (без DTLS/WG), например 0.0.0.0:56013")\n'
            '\tflagNoNAT := flag.Bool("no-nat", false, "skip iptables/nft NAT (awg-manager на роутере)")\n'
            '\tflagWGIface := flag.String("wg-iface", "", "userspace WG iface (opkgtunN для Keenetic)")\n'
            '\tflagNatIface := flag.String("nat-if", "", "egress interface for MASQUERADE")\n'
            '\tflag.Parse()',
        )

        text = text.replace(
            "\tdns = *dnsFlag\n\n\tlog.SetFlags",
            "\tdns = *dnsFlag\n"
            "\tkeeneticNoNAT = *flagNoNAT\n"
            "\tkeeneticNatIface = strings.TrimSpace(*flagNatIface)\n"
            "\tif n := strings.TrimSpace(*flagWGIface); n != \"\" {\n"
            "\t\twgIfaceName = n\n"
            "\t}\n\n"
            "\tlog.SetFlags",
        )

        insert_after_wg = (
            "\twgDev, err = startUserspaceWG(keys, *wgPort)\n"
            "\tif err != nil {\n"
            "\t\tlog.Fatalf(\"[WG] Запуск: %v\", err)\n"
            "\t}\n"
            "\tglobalWgDev = wgDev\n"
        )
        extensions = (
            "\n\tif directListen := strings.TrimSpace(*flagListenDirect); directListen != \"\" {\n"
            "\t\tgo func() {\n"
            "\t\t\tif err := runDirectServer(ctx, directListen, fmt.Sprintf(\"127.0.0.1:%d\", *wgPort), wgDev, keys); err != nil && ctx.Err() == nil {\n"
            "\t\t\t\tlog.Fatalf(\"[DIRECT] %v\", err)\n"
            "\t\t\t}\n"
            "\t\t}()\n"
            "\t}\n"
            "\tif rawListen := strings.TrimSpace(*flagListenRaw); rawListen != \"\" {\n"
            "\t\tgo func() {\n"
            "\t\t\tif err := runRawServer(ctx, rawListen); err != nil && ctx.Err() == nil {\n"
            "\t\t\t\tlog.Fatalf(\"[RAW] %v\", err)\n"
            "\t\t\t}\n"
            "\t\t}()\n"
            "\t}\n\n"
        )
        if insert_after_wg in text and extensions.strip() not in text:
            text = text.replace(insert_after_wg, insert_after_wg + extensions, 1)

        text = text.replace(
            "func setupFullConeNAT(wgIface string) error {",
            "func setupFullConeNAT(wgIface string) error {\n\tif keeneticNoNAT {\n"
            "\t\tlog.Println(\"[NAT] пропуск (-no-nat)\")\n"
            "\t\tnatType = \"disabled (-no-nat)\"\n"
            "\t\treturn nil\n"
            "\t}\n",
        )

        # NAT iface override when not -no-nat
        text = text.replace(
            "extIface := getDefaultInterface()",
            'extIface := keeneticNatIface\n\tif extIface == "" {\n\t\textIface = getDefaultInterface()\n\t}',
            1,
        )

        # Log line with direct/raw ports
        text = text.replace(
            '\tlog.Printf("   DTLS: %s | WG: %s | NAT: %s", *listen, wgEndpoint, natType)',
            '\tports := fmt.Sprintf("DTLS: %s | WG: %s", *listen, wgEndpoint)\n'
            '\tif d := strings.TrimSpace(*flagListenDirect); d != "" {\n'
            '\t\tports += fmt.Sprintf(" | DIRECT: %s", d)\n'
            '\t}\n'
            '\tif r := strings.TrimSpace(*flagListenRaw); r != "" {\n'
            '\t\tports += fmt.Sprintf(" | RAW: %s", r)\n'
            '\t}\n'
            '\tlog.Printf("   %s | NAT: %s", ports, natType)',
        )

    text = patch_listen_wrapped_batch(text)
    text = patch_stats_active_devices(text)
    text = patch_dtls_idle_timeout(text)
    text = patch_get_next_ip_skip_gateway(text)
    return text


def patch_get_next_ip_skip_gateway(text: str) -> str:
    """OpkgTun gateway on Keenetic is 10.66.0.1 — must not be assigned to clients."""
    old = '\t\t\tif ip == "10.66.66.1" {\n\t\t\t\tcontinue\n\t\t\t}'
    new = (
        '\t\t\tif ip == "10.66.66.1" || ip == "10.66.0.1" {\n'
        "\t\t\t\tcontinue\n"
        "\t\t\t}"
    )
    if old not in text:
        if 'ip == "10.66.0.1"' in text:
            return text
        raise SystemExit("getNextIP gateway patch: anchor not found in server.go")
    return text.replace(old, new, 1)


def patch_stats_active_devices(text: str) -> str:
    """[СТАТ] Активных — число устройств, не UDP relay-сессий."""
    if "countActiveDevices()" in text:
        return text
    old = "atomic.LoadInt32(&activeConns)"
    new = "countActiveDevices()"
    if old not in text:
        return text
    return text.replace(old, new)


def patch_dtls_idle_timeout(text: str) -> str:
    """DTLS relay: 90s idle вместо 30min — иначе [СТАТ] залипает после отключения клиента."""
    if "dtlsConnIdle" in text:
        return text
    anchor = "func handleConn("
    if anchor not in text:
        return text
    if "const dtlsConnIdle" not in text:
        text = text.replace(
            "func handleConn(",
            "const dtlsConnIdle = 90 * time.Second\n\nfunc handleConn(",
            1,
        )
    text = text.replace("30*time.Minute", "dtlsConnIdle")
    text = text.replace("30 * time.Minute", "dtlsConnIdle")
    return text


def patch_listen_wrapped_batch(text: str) -> str:
    """APK qWDTT 1.4: pion udp BatchIO + большие socket buffers (messageBatch в бинарнике)."""
    if "BatchIOConfig" in text:
        return text
    old = (
        '\tinner, err := pionudp.Listen("udp", addr)\n'
        '\tif err != nil {\n'
        '\t\treturn nil, fmt.Errorf("wrap: udp listen: %w", err)\n'
        '\t}'
    )
    new = (
        '\tinner, err := (&pionudp.ListenConfig{\n'
        '\t\tReadBufferSize:  8 << 20,\n'
        '\t\tWriteBufferSize: 2 << 20,\n'
		'\t\tBatch: pionudp.BatchIOConfig{\n'
		'\t\t\tEnable:             true,\n'
		'\t\t\tReadBatchSize:      64,\n'
		'\t\t\tWriteBatchSize:     64,\n'
		'\t\t\tWriteBatchInterval: 200 * time.Microsecond,\n'
		'\t\t},\n'
        '\t}).Listen("udp", addr)\n'
        '\tif err != nil {\n'
        '\t\treturn nil, fmt.Errorf("wrap: udp listen: %w", err)\n'
        '\t}'
    )
    if old not in text:
        return text
    return text.replace(old, new, 1)


def adapt_server_raw(text: str) -> str:
    text = text.replace("package server\n", "package main\n")
    text = re.sub(
        r"func runRawServer\(ctx context\.Context, cfg ServerConfig, listenAddr string\) error \{",
        "func runRawServer(ctx context.Context, listenAddr string) error {",
        text,
    )
    text = text.replace("setupRawNAT(cfg.NoNAT, cfg.NatIface)", "setupRawNAT(keeneticNoNAT, keeneticNatIface)")
    text = text.replace("setupRawMSSClamping(cfg.NoNAT)", "setupRawMSSClamping(keeneticNoNAT)")
    text = text.replace("defaultClientDNS", '"8.8.8.8"')
    text = text.replace("clientDNS", "dns")
    text = text.replace("\tuserTouchActivity(sess.deviceID)\n", "")

    # Monolith auth (passwords.json) instead of ildarmaga panel.db helpers.
    old_auth = """\twrapPass := lookupWrapAuth(remote)
\tif wrapPass != "" && password != wrapPass {
\t\treturn "", "DENIED:wrong_password", ""
\t}

\tdbMutex.Lock()
\tisMainPass := password != "" && password == db.MainPassword
\tentry, isGenPass := db.Passwords[password]
\tvalid := isMainPass || (isGenPass && !isPasswordExpired(entry) && !isTrafficExceeded(entry))

\tif !valid {
\t\tdbMutex.Unlock()
\t\tif isGenPass && isTrafficExceeded(entry) {
\t\t\treturn "", "DENIED:traffic_exceeded", ""
\t\t}
\t\tif isGenPass && isPasswordExpired(entry) {
\t\t\treturn "", "DENIED:expired", ""
\t\t}
\t\treturn "", "DENIED:wrong_password", ""
\t}
\tif isGenPass && entry.IsDeactivated {
\t\tdbMutex.Unlock()
\t\treturn "", "DENIED:deactivated", ""
\t}
\tif isGenPass && !isMainPass && !entryCanAcceptDevice(entry, deviceID) {
\t\tdbMutex.Unlock()
\t\treturn "", "DENIED:device_mismatch", ""
\t}

\tif isGenPass && !isMainPass && !entryHasDevice(entry, deviceID) {
\t\tif bindDeviceToEntry(entry, deviceID) {
\t\t\t_ = persistUserBindingsSQLiteLocked(password, entry)
\t\t}
\t}
\tif isMainPass {
\t\tensureMainPasswordEntryLocked()
\t}
\tdbMutex.Unlock()"""

    new_auth = """\tdbMutex.Lock()
\tisMainPass := password != "" && password == db.MainPassword
\tentry, isGenPass := db.Passwords[password]
\tvalid := isMainPass || (isGenPass && !isPasswordExpired(entry))

\tif !valid {
\t\tdbMutex.Unlock()
\t\tif isGenPass && isPasswordExpired(entry) {
\t\t\treturn "", "DENIED:expired", ""
\t\t}
\t\treturn "", "DENIED:wrong_password", ""
\t}
\tif isGenPass && entry.IsDeactivated {
\t\tdbMutex.Unlock()
\t\treturn "", "DENIED:deactivated", ""
\t}
\tif isGenPass && !entry.canConnectAndBind(deviceID) {
\t\tdbMutex.Unlock()
\t\treturn "", "DENIED:device_mismatch", ""
\t}
\tif isGenPass {
\t\tsaveDB()
\t}
\tdbMutex.Unlock()"""

    if old_auth in text:
        text = text.replace(old_auth, new_auth)
    return text


def write_keenetic_globals(path: pathlib.Path) -> None:
    if path.exists():
        return
    path.write_text(
        "package main\n\n"
        "var (\n"
        "\tkeeneticNoNAT   bool\n"
        "\tkeeneticNatIface string\n"
        ")\n\n"
        "// countActiveDevices — устройства с активной сессией (WG/raw), не relay UDP.\n"
        "func countActiveDevices() int32 {\n"
        "\tactiveDevicesMu.Lock()\n"
        "\tn := int32(len(activeDevices))\n"
        "\tactiveDevicesMu.Unlock()\n"
        "\treturn n\n"
        "}\n",
        encoding="utf-8",
    )


def main() -> int:
    server_go = ROOT / "server.go"
    if not server_go.exists():
        raise SystemExit(f"server.go not found in {ROOT}")
    original = server_go.read_text(encoding="utf-8")
    patched = patch_server_go(original)
    server_go.write_text(patched, encoding="utf-8")
    write_keenetic_globals(ROOT / "keenetic.go")

    if is_v14_monolith(original):
        print(f"patched v1.4 monolith in {ROOT} (keenetic flags; upstream raw/direct + pacer)")
        return 0

    shutil.copy2(PATCH_DIR / "server_direct.go", ROOT / "server_direct.go")

    if not SRC_RAW.exists():
        raise SystemExit(f"missing {SRC_RAW}")
    raw = adapt_server_raw(SRC_RAW.read_text(encoding="utf-8"))
    (ROOT / "server_raw.go").write_text(raw, encoding="utf-8")

    print(f"patched legacy monolith in {ROOT} (direct + raw + keenetic flags)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
