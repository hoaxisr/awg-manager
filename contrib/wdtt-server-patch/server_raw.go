package server

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.zx2c4.com/wireguard/tun"
)

const (
	rawIfaceName         = "wdttraw0"
	rawServerAddr        = "10.70.66.1"
	rawServerCIDR        = rawServerAddr + "/16"
	rawMTU               = 1300
	rawIPPrefix          = "10.70.66."
	rawTunVirtioHdrLen   = 10 // wireguard-go tun.virtioNetHdrLen on Linux (amd64/arm64)
	rawDownlinkQueueSize = 256  // как в APK (newDownlinkWorker)
	rawDownlinkChunkSize = 8    // downlinkChunkSizeFor в APK
	rawMaxWorkersPerIP   = 256  // safety cap (support up to 256 parallel workers)
	rawPacketBufCap      = 2048
	rawConnIdleTimeout   = 90 * time.Second // быстрый сброс [СТАТ] после отключения клиента
)

var (
	rawDownlinkBufPool sync.Pool
	rawTunWriteBufPool sync.Pool
)

func init() {
	rawDownlinkBufPool.New = func() any {
		b := make([]byte, rawPacketBufCap)
		return &b
	}
	rawTunWriteBufPool.New = func() any {
		b := make([]byte, rawTunVirtioHdrLen+rawPacketBufCap)
		return &b
	}
}

type rawClientSession struct {
	deviceID string
	clientIP string
	conn     net.PacketConn
	remote   net.Addr
	ready    bool // сессия готова принимать uplink (GETCONF или device уже авторизован)
}

type rawClientSessions struct {
	mu      sync.Mutex
	workers []*downlinkWorker
	seq     uint32
}

type rawRouter struct {
	tun           tun.Device
	tunName       string
	mu            sync.RWMutex
	sessions      map[string]*rawClientSessions // client IP → workers
	byDevice      map[string]*rawClientSession
	authorizedIPs map[string]string
}

type downlinkWorker struct {
	router *rawRouter
	sess   *rawClientSession
	ch     chan *[]byte
}

func newRawRouter(t tun.Device, tunName string) *rawRouter {
	return &rawRouter{
		tun:           t,
		tunName:       tunName,
		sessions:      make(map[string]*rawClientSessions),
		byDevice:      make(map[string]*rawClientSession),
		authorizedIPs: make(map[string]string),
	}
}

func downlinkChunkSizeFor(nRelays int) uint32 {
	if nRelays <= 0 {
		return rawDownlinkChunkSize
	}
	return rawDownlinkChunkSize
}

func (r *rawRouter) sessionsFor(clientIP string) *rawClientSessions {
	r.mu.RLock()
	cs := r.sessions[clientIP]
	r.mu.RUnlock()
	if cs != nil {
		return cs
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if cs = r.sessions[clientIP]; cs != nil {
		return cs
	}
	cs = &rawClientSessions{}
	r.sessions[clientIP] = cs
	return cs
}

func newDownlinkWorker(router *rawRouter, sess *rawClientSession) *downlinkWorker {
	return &downlinkWorker{
		router: router,
		sess:   sess,
		ch:     make(chan *[]byte, rawDownlinkQueueSize),
	}
}

func (w *downlinkWorker) enqueue(bp *[]byte) bool {
	select {
	case w.ch <- bp:
		return true
	default:
		return false
	}
}

func (w *downlinkWorker) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case bp, ok := <-w.ch:
			if !ok {
				return
			}
			buf := *bp
			if len(buf) >= 20 && w.sess != nil && w.sess.ready {
				if err := w.router.writeDownlinkPacket(buf, w.sess); err == nil {
					n := len(buf)
					atomic.AddInt64(&totalBytesToClient, int64(n))
					if atomic.CompareAndSwapInt32(&rawFirstDownlinkLogged, 0, 1) {
						log.Printf("[RAW] Первый downlink-пакет доставлен клиенту %s (%d байт)", w.sess.remote, n)
					}
				} else {
					w.sess.ready = false
					go w.router.unregister(w.sess)
				}
			}
			putRawDownlinkBuf(bp)
		}
	}
}

func (w *downlinkWorker) stop() {
	// drain не нужен — conn закрывается отдельно
	close(w.ch)
}

func getRawDownlinkBuf(n int) *[]byte {
	bp := rawDownlinkBufPool.Get().(*[]byte)
	buf := *bp
	if cap(buf) < n {
		buf = make([]byte, n)
		*bp = buf
	}
	*bp = buf[:n]
	return bp
}

func putRawDownlinkBuf(bp *[]byte) {
	if cap(*bp) < rawPacketBufCap/2 {
		return
	}
	*bp = (*bp)[:0]
	rawDownlinkBufPool.Put(bp)
}

func (r *rawRouter) dispatchDownlink(packet []byte) {
	dst := ipv4DstString(packet)
	w := r.pickDownlinkWorker(packet)
	if w == nil {
		if dst != "" && atomic.CompareAndSwapInt32(&rawNoDownlinkSessionLogged, 0, 1) {
			log.Printf("[RAW] downlink: нет сессии для %s (пакет от интернета, но клиент не зарегистрирован)", dst)
		}
		return
	}
	bp := getRawDownlinkBuf(len(packet))
	copy(*bp, packet)
	if !w.enqueue(bp) {
		putRawDownlinkBuf(bp)
	}
}

func (r *rawRouter) authorizeClientIP(clientIP, deviceID string) {
	r.mu.Lock()
	r.authorizedIPs[clientIP] = deviceID
	r.mu.Unlock()
}

func (r *rawRouter) lookupAuthorizedClient(clientIP string) (deviceID string, ok bool) {
	r.mu.RLock()
	deviceID, ok = r.authorizedIPs[clientIP]
	r.mu.RUnlock()
	return deviceID, ok
}

// evictRelaysForIP закрывает все relay на clientIP (переподключение клиента).
func (r *rawRouter) evictRelaysForIP(clientIP string) {
	r.mu.Lock()
	cs := r.sessions[clientIP]
	var workers []*downlinkWorker
	var relays []*rawClientSession
	if cs != nil {
		cs.mu.Lock()
		workers = append([]*downlinkWorker(nil), cs.workers...)
		for _, w := range cs.workers {
			if w != nil && w.sess != nil {
				relays = append(relays, w.sess)
			}
		}
		cs.workers = nil
		cs.mu.Unlock()
		delete(r.sessions, clientIP)
	}
	for _, s := range relays {
		delete(r.byDevice, s.deviceID)
	}
	n := len(relays)
	r.mu.Unlock()
	for _, w := range workers {
		if w != nil {
			w.stop()
		}
	}
	if n == 0 {
		return
	}
	rawIPRefReset(clientIP)
	for _, s := range relays {
		if s != nil && s.conn != nil {
			_ = s.conn.Close()
		}
	}
	log.Printf("[RAW] Сброшены старые relay для %s (%d шт.)", clientIP, n)
}

func (r *rawRouter) register(ctx context.Context, sess *rawClientSession) bool {
	cs := r.sessionsFor(sess.clientIP)
	cs.mu.Lock()
	for _, w := range cs.workers {
		if w != nil && w.sess != nil && w.sess.remote.String() == sess.remote.String() {
			cs.mu.Unlock()
			return false
		}
	}
	for len(cs.workers) >= rawMaxWorkersPerIP {
		old := cs.workers[0]
		cs.workers = cs.workers[1:]
		cs.mu.Unlock()
		if old != nil {
			old.stop()
			if old.sess != nil && old.sess.conn != nil {
				_ = old.sess.conn.Close()
			}
		}
		cs.mu.Lock()
	}
	firstRelay := len(cs.workers) == 0
	w := newDownlinkWorker(r, sess)
	cs.workers = append(cs.workers, w)
	cs.mu.Unlock()
	go w.run(ctx)

	r.mu.Lock()
	r.byDevice[sess.deviceID] = sess
	r.mu.Unlock()
	if firstRelay {
		rawIPRefInc(sess.clientIP)
		if sess.deviceID != "" {
			rawDeviceActiveInc(sess.deviceID)
		}
	}
	return true
}

func (r *rawRouter) unregister(sess *rawClientSession) {
	cs := r.sessionsFor(sess.clientIP)
	cs.mu.Lock()
	out := cs.workers[:0]
	var stopped *downlinkWorker
	for _, w := range cs.workers {
		if w != nil && w.sess == sess {
			stopped = w
			continue
		}
		out = append(out, w)
	}
	cs.workers = out
	empty := len(out) == 0
	cs.mu.Unlock()
	if stopped != nil {
		stopped.stop()
	}

	r.mu.Lock()
	if cur, ok := r.byDevice[sess.deviceID]; ok && cur == sess {
		delete(r.byDevice, sess.deviceID)
	}
	if empty {
		delete(r.sessions, sess.clientIP)
	}
	r.mu.Unlock()

	if stopped != nil && empty && sess.clientIP != "" {
		rawIPRefDec(sess.clientIP)
		rawDeviceActiveDec(sess.deviceID)
	}
}

func (r *rawRouter) pickDownlinkWorker(packet []byte) *downlinkWorker {
	dst := ipv4DstString(packet)
	if dst == "" {
		return nil
	}
	cs := r.sessionsFor(dst)
	cs.mu.Lock()
	defer cs.mu.Unlock()
	n := len(cs.workers)
	if n == 0 {
		return nil
	}
	chunk := downlinkChunkSizeFor(n)
	seq := atomic.AddUint32(&cs.seq, 1) - 1
	start := int(seq/chunk) % n
	for i := 0; i < n; i++ {
		w := cs.workers[(start+i)%n]
		if w != nil && w.sess != nil && w.sess.ready {
			return w
		}
	}
	return nil
}

func (r *rawRouter) writeDownlinkPacket(packet []byte, sess *rawClientSession) error {
	if sess == nil || !sess.ready {
		return fmt.Errorf("session not ready")
	}
	_, err := sess.conn.WriteTo(packet, sess.remote)
	return err
}

func ipv4DstString(packet []byte) string {
	if len(packet) < 20 || packet[0]>>4 != 4 {
		return ""
	}
	ihl := int(packet[0]&0x0f) * 4
	if len(packet) < ihl {
		return ""
	}
	return net.IP(packet[ihl-4 : ihl]).String()
}

func ipv4SrcString(packet []byte) string {
	if len(packet) < 20 || packet[0]>>4 != 4 {
		return ""
	}
	return net.IP(packet[12:16]).String()
}

// writePacketToTun пишет IPv4-пакет в TUN. CreateTUN включает IFF_VNET_HDR;
// Write() требует offset >= rawTunVirtioHdrLen (см. wireguard-go tun_linux.go).
func writePacketToTun(dev tun.Device, payload []byte) error {
	if len(payload) == 0 {
		return nil
	}
	if dev.BatchSize() > 1 {
		bp := rawTunWriteBufPool.Get().(*[]byte)
		buf := *bp
		need := rawTunVirtioHdrLen + len(payload)
		if cap(buf) < need {
			buf = make([]byte, need)
			*bp = buf
		}
		buf = buf[:need]
		copy(buf[rawTunVirtioHdrLen:], payload)
		_, err := dev.Write([][]byte{buf}, rawTunVirtioHdrLen)
		rawTunWriteBufPool.Put(bp)
		return err
	}
	_, err := dev.Write([][]byte{payload}, 0)
	return err
}

func (r *rawRouter) downlinkLoop(ctx context.Context) {
	batchSize := r.tun.BatchSize()
	if batchSize < 1 {
		batchSize = 1
	}
	bufs := make([][]byte, batchSize)
	sizes := make([]int, batchSize)
	for i := range bufs {
		bufs[i] = make([]byte, rawTunVirtioHdrLen+rawPacketBufCap)
	}
	for {
		select {
		case <-ctx.Done():
			log.Printf("[RAW] downlink остановлен: %v", ctx.Err())
			return
		default:
		}
		nPkts, err := r.tun.Read(bufs, sizes, rawTunVirtioHdrLen)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
			continue
		}
		for i := 0; i < nPkts; i++ {
			n := sizes[i]
			if n <= 0 {
				continue
			}
			packet := bufs[i][rawTunVirtioHdrLen : rawTunVirtioHdrLen+n]
			if len(packet) < 20 || packet[0]>>4 != 4 {
				continue
			}
			r.dispatchDownlink(packet)
		}
	}
}

var rawNoSessionLogged int32
var rawNoDownlinkSessionLogged int32
var rawFirstDownlinkLogged int32
var rawDeviceIPs sync.Map // deviceID -> raw client IP (10.70.66.x)
var rawIPRefs sync.Map    // clientIP -> *int32 (refcount для [СТАТ] Активных)

func rawIPRefInc(clientIP string) {
	if clientIP == "" {
		return
	}
	v, _ := rawIPRefs.LoadOrStore(clientIP, new(int32))
	ref := v.(*int32)
	if atomic.AddInt32(ref, 1) == 1 {
		atomic.AddInt32(&activeConns, 1)
	}
}

func rawIPRefDec(clientIP string) {
	if clientIP == "" {
		return
	}
	v, ok := rawIPRefs.Load(clientIP)
	if !ok {
		return
	}
	ref := v.(*int32)
	if atomic.AddInt32(ref, -1) <= 0 {
		rawIPRefs.Delete(clientIP)
		atomic.AddInt32(&activeConns, -1)
	}
}

func rawIPRefReset(clientIP string) {
	if clientIP == "" {
		return
	}
	if v, ok := rawIPRefs.LoadAndDelete(clientIP); ok && atomic.LoadInt32(v.(*int32)) > 0 {
		atomic.AddInt32(&activeConns, -1)
	}
}

func rawDeviceActiveInc(deviceID string) {
	if deviceID == "" {
		return
	}
	activeDevicesMu.Lock()
	activeDevices[deviceID]++
	activeDevicesMu.Unlock()
}

func rawDeviceActiveDec(deviceID string) {
	if deviceID == "" {
		return
	}
	activeDevicesMu.Lock()
	activeDevices[deviceID]--
	if activeDevices[deviceID] <= 0 {
		delete(activeDevices, deviceID)
	}
	activeDevicesMu.Unlock()
}

func tuneRawSysctl() {
	for _, kv := range []struct{ path, val string }{
		{"/proc/sys/net/core/rmem_max", "25165824"},
		{"/proc/sys/net/core/wmem_max", "25165824"},
	} {
		_ = os.WriteFile(kv.path, []byte(kv.val), 0o644)
	}
}

func runRawServer(ctx context.Context, cfg ServerConfig, listenAddr string) error {
	tuneRawSysctl()
	runCmdSilent("ip", "link", "del", rawIfaceName)
	time.Sleep(100 * time.Millisecond)

	tunDev, err := tun.CreateTUN(rawIfaceName, rawMTU)
	if err != nil {
		return fmt.Errorf("CreateTUN: %w", err)
	}
	tunName, err := tunDev.Name()
	if err != nil {
		tunDev.Close()
		return fmt.Errorf("TUN name: %w", err)
	}
	if err := configureRawInterface(tunName); err != nil {
		tunDev.Close()
		return err
	}
	if err := setupRawNAT(cfg.NoNAT, cfg.NatIface); err != nil {
		tunDev.Close()
		return err
	}
	setupRawMSSClamping(cfg.NoNAT)

	defer func() {
		cleanupRawNAT()
		runCmdSilent("ip", "link", "del", tunName)
		tunDev.Close()
	}()

	log.Printf("[RAW] TUN %s поднят (%s), MTU %d", tunName, rawServerCIDR, rawMTU)
	log.Printf("   RAW (без WireGuard, без DTLS): %s", listenAddr)

	router := newRawRouter(tunDev, tunName)
	go router.downlinkLoop(ctx)

	addr, err := net.ResolveUDPAddr("udp", listenAddr)
	if err != nil {
		return fmt.Errorf("адрес %s: %w", listenAddr, err)
	}
	if serverWrapKeys.Count() == 0 {
		return fmt.Errorf("нет активных паролей для WRAP")
	}

	pl, err := listenWrapped(addr, serverWrapKeys)
	if err != nil {
		return err
	}
	context.AfterFunc(ctx, func() { _ = pl.Close() })

	var acceptWg sync.WaitGroup
	for {
		pc, remote, err := pl.Accept()
		if err != nil {
			if ctx.Err() != nil {
				acceptWg.Wait()
				return nil
			}
			continue
		}
		acceptWg.Add(1)
		go func(c net.PacketConn, raddr net.Addr) {
			defer acceptWg.Done()
			handleRawPacketConn(ctx, c, raddr, router)
		}(pc, remote)
	}
}

func configureRawInterface(ifaceName string) error {
	for _, cmd := range [][]string{
		{"ip", "addr", "add", rawServerCIDR, "dev", ifaceName},
		{"ip", "link", "set", "mtu", strconv.Itoa(rawMTU), "dev", ifaceName},
		{"ip", "link", "set", ifaceName, "up"},
	} {
		out, err := runCmd(cmd[0], cmd[1:]...)
		if err != nil && !strings.Contains(out, "File exists") {
			return fmt.Errorf("%s: %s", strings.Join(cmd, " "), out)
		}
	}
	return nil
}

func setupRawNAT(noNAT bool, natIface string) error {
	log.Println("[RAW-NAT] ══════════════════════════════════════")
	if noNAT {
		log.Println("[RAW-NAT] пропуск (-no-nat)")
		log.Println("[RAW-NAT] ══════════════════════════════════════")
		return nil
	}
	os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0644)
	setupRawForwardRules()
	extIface := strings.TrimSpace(natIface)
	if extIface == "" {
		extIface = getDefaultInterface()
	}
	log.Printf("[RAW-NAT] Внешний: %s", extIface)
	if commandExists("iptables") {
		for i := 0; i < 5; i++ {
			exec.Command("iptables", "-t", "nat", "-D", "POSTROUTING", "-s", rawServerCIDR, "-o", extIface,
				"-m", "comment", "--comment", "WDTT_RAW_MANAGED", "-j", "MASQUERADE").Run()
		}
		exec.Command("iptables", "-t", "nat", "-I", "POSTROUTING", "1", "-s", rawServerCIDR, "-o", extIface,
			"-m", "comment", "--comment", "WDTT_RAW_MANAGED", "-j", "MASQUERADE").Run()
	}
	log.Println("[RAW-NAT] ══════════════════════════════════════")
	return nil
}

func setupRawForwardRules() {
	if !commandExists("iptables") {
		return
	}
	for i := 0; i < 5; i++ {
		exec.Command("iptables", "-D", "FORWARD", "-i", rawIfaceName, "-m", "comment", "--comment", "WDTT_RAW_MANAGED", "-j", "ACCEPT").Run()
		exec.Command("iptables", "-D", "FORWARD", "-o", rawIfaceName, "-m", "comment", "--comment", "WDTT_RAW_MANAGED", "-j", "ACCEPT").Run()
	}
	exec.Command("iptables", "-I", "FORWARD", "1", "-i", rawIfaceName, "-m", "comment", "--comment", "WDTT_RAW_MANAGED", "-j", "ACCEPT").Run()
	exec.Command("iptables", "-I", "FORWARD", "1", "-o", rawIfaceName, "-m", "comment", "--comment", "WDTT_RAW_MANAGED", "-j", "ACCEPT").Run()
}

func setupRawMSSClamping(noNAT bool) {
	if noNAT || !commandExists("iptables") {
		return
	}
	exec.Command("iptables", "-t", "mangle", "-N", "wdttraw_mangle").Run()
	exec.Command("iptables", "-t", "mangle", "-F", "wdttraw_mangle").Run()
	// Как deploy.sh qWDTT: clamp по подсети клиентов (-s и -d), не только -o iface.
	for _, spec := range []string{"-s", "-d"} {
		exec.Command("iptables", "-t", "mangle", "-A", "wdttraw_mangle", spec, rawServerCIDR,
			"-p", "tcp", "--tcp-flags", "SYN,RST", "SYN", "-j", "TCPMSS", "--clamp-mss-to-pmtu").Run()
	}
	exec.Command("iptables", "-t", "mangle", "-D", "FORWARD", "-j", "wdttraw_mangle").Run()
	exec.Command("iptables", "-t", "mangle", "-I", "FORWARD", "1", "-j", "wdttraw_mangle").Run()
}

func cleanupRawNAT() {
	if commandExists("iptables") {
		for i := 0; i < 5; i++ {
			exec.Command("iptables", "-t", "nat", "-D", "POSTROUTING", "-s", rawServerCIDR,
				"-m", "comment", "--comment", "WDTT_RAW_MANAGED", "-j", "MASQUERADE").Run()
			exec.Command("iptables", "-D", "FORWARD", "-i", rawIfaceName, "-m", "comment", "--comment", "WDTT_RAW_MANAGED", "-j", "ACCEPT").Run()
			exec.Command("iptables", "-D", "FORWARD", "-o", rawIfaceName, "-m", "comment", "--comment", "WDTT_RAW_MANAGED", "-j", "ACCEPT").Run()
		}
		exec.Command("iptables", "-t", "mangle", "-D", "FORWARD", "-j", "wdttraw_mangle").Run()
		exec.Command("iptables", "-t", "mangle", "-F", "wdttraw_mangle").Run()
		exec.Command("iptables", "-t", "mangle", "-X", "wdttraw_mangle").Run()
	}
}

func allocRawIP(deviceID string) string {
	if v, ok := rawDeviceIPs.Load(deviceID); ok {
		if ip, _ := v.(string); ip != "" {
			return ip
		}
	}
	ip := nextFreeRawIP()
	if ip == "" {
		return ""
	}
	rawDeviceIPs.Store(deviceID, ip)
	return ip
}

func nextFreeRawIP() string {
	used := make(map[string]bool)
	rawDeviceIPs.Range(func(_, v any) bool {
		if ip, ok := v.(string); ok && ip != "" {
			used[ip] = true
		}
		return true
	})
	for i := 2; i <= 250; i++ {
		ip := fmt.Sprintf("%s%d", rawIPPrefix, i)
		if !used[ip] {
			return ip
		}
	}
	return ""
}

func buildRawConf(clientIP string) string {
	dns := strings.TrimSpace(clientDNS)
	if dns == "" {
		dns = defaultClientDNS
	}
	return fmt.Sprintf("RAWCONF:%s|%s|%d", clientIP, dns, rawMTU)
}

func handleRawPacketConn(ctx context.Context, pc net.PacketConn, remote net.Addr, router *rawRouter) {
	atomic.AddInt64(&totalConns, 1)
	buf := make([]byte, 2048)
	gotConf := false
	var sess *rawClientSession
	registered := false

	defer func() {
		if sess != nil {
			router.unregister(sess)
		}
		if registered && sess != nil {
			log.Printf("[RAW] Сессия %s (ip=%s) завершена", sess.deviceID, sess.clientIP)
		}
		_ = pc.Close()
	}()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		_ = pc.SetReadDeadline(time.Now().Add(rawConnIdleTimeout))
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		if n <= 0 {
			continue
		}
		payload := buf[:n]

		if !gotConf {
			msg := strings.TrimSpace(string(payload))
			if strings.HasPrefix(msg, "GETCONF_RAW:") {
				deviceID, password, ok := parseGetConfRaw(msg)
				if !ok {
					continue
				}
				clientIP, denied, errMsg := authorizeRawConfig(deviceID, password, remote)
				if denied != "" {
					_, _ = pc.WriteTo([]byte(denied), remote)
					return
				}
				if clientIP == "" {
					if errMsg != "" {
						_, _ = pc.WriteTo([]byte(errMsg), remote)
					}
					return
				}
				resp := buildRawConf(clientIP)
				if _, err := pc.WriteTo([]byte(resp), remote); err != nil {
					return
				}
				gotConf = true
				// GETCONF_RAW шлёт только worker #1 при каждом connect — сброс stale relay.
				router.evictRelaysForIP(clientIP)
				sess = &rawClientSession{
					deviceID: deviceID,
					clientIP: clientIP,
					conn:     pc,
					remote:   remote,
					ready:    true,
				}
				router.authorizeClientIP(clientIP, deviceID)
				if !router.register(ctx, sess) {
					return
				}
				registered = true
				atomic.StoreInt32(&rawNoSessionLogged, 0)
				log.Printf("[RAW] Сессия %s зарегистрирована (ip=%s, getConf=true)", deviceID, clientIP)
				continue
			}
			// Официальный qWDTT: GETCONF_RAW один раз на устройство; остальные relay шлют IP сразу.
			if len(payload) >= 20 && payload[0]>>4 == 4 {
				src := ipv4SrcString(payload)
				if deviceID, ok := router.lookupAuthorizedClient(src); ok {
					gotConf = true
					sess = &rawClientSession{
						deviceID: deviceID,
						clientIP: src,
						conn:     pc,
						remote:   remote,
						ready:    true,
					}
					if !router.register(ctx, sess) {
						return
					}
					registered = true
					atomic.StoreInt32(&rawNoSessionLogged, 0)
					log.Printf("[RAW] Relay %v → ip=%s (device=%s, uplink без GETCONF)", remote, src, deviceID)
				} else {
					continue
				}
			} else {
				continue
			}
		}

		if len(payload) >= 20 && payload[0]>>4 == 4 {
			if sess == nil || !sess.ready {
				continue
			}
			atomic.AddInt64(&totalBytesFromClient, int64(n))
			userTouchActivity(sess.deviceID)
			if err := writePacketToTun(router.tun, payload); err != nil {
				log.Printf("[RAW] Ошибка записи в TUN (ip=%s): %v", clientIPOrDash(sess), err)
			} else if atomic.CompareAndSwapInt32(&rawFirstUplinkLogged, 0, 1) {
				log.Printf("[RAW] Первый uplink-пакет от %s записан в TUN (%d байт)", remote, n)
			}
		}
	}
}

func clientIPOrDash(sess *rawClientSession) string {
	if sess == nil {
		return "-"
	}
	return sess.clientIP
}

var rawFirstUplinkLogged int32

func parseGetConfRaw(msg string) (deviceID, password string, ok bool) {
	parts := strings.Split(strings.TrimSpace(strings.TrimPrefix(msg, "GETCONF_RAW:")), "|")
	if len(parts) < 2 {
		return "", "", false
	}
	deviceID = strings.TrimSpace(parts[0])
	password = strings.TrimSpace(parts[1])
	if deviceID == "" {
		deviceID = "unknown"
	}
	return deviceID, password, password != ""
}

func authorizeRawConfig(deviceID, password string, remote net.Addr) (clientIP, denied, errMsg string) {
	wrapPass := lookupWrapAuth(remote)
	if wrapPass != "" && password != wrapPass {
		return "", "DENIED:wrong_password", ""
	}

	dbMutex.Lock()
	isMainPass := password != "" && password == db.MainPassword
	entry, isGenPass := db.Passwords[password]
	valid := isMainPass || (isGenPass && !isPasswordExpired(entry) && !isTrafficExceeded(entry))

	if !valid {
		dbMutex.Unlock()
		if isGenPass && isTrafficExceeded(entry) {
			return "", "DENIED:traffic_exceeded", ""
		}
		if isGenPass && isPasswordExpired(entry) {
			return "", "DENIED:expired", ""
		}
		return "", "DENIED:wrong_password", ""
	}
	if isGenPass && entry.IsDeactivated {
		dbMutex.Unlock()
		return "", "DENIED:deactivated", ""
	}
	if isGenPass && !isMainPass && !entryCanAcceptDevice(entry, deviceID) {
		dbMutex.Unlock()
		return "", "DENIED:device_mismatch", ""
	}

	if isGenPass && !isMainPass && !entryHasDevice(entry, deviceID) {
		if bindDeviceToEntry(entry, deviceID) {
			_ = persistUserBindingsSQLiteLocked(password, entry)
		}
	}
	if isMainPass {
		ensureMainPasswordEntryLocked()
	}
	dbMutex.Unlock()

	clientIP = allocRawIP(deviceID)
	if clientIP == "" {
		return "", "", "NOCONF"
	}
	log.Printf("[RAW] Устройство %s → raw IP %s", deviceID, clientIP)
	return clientIP, "", ""
}
