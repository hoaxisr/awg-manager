package realityserver

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hoaxisr/awg-manager/internal/xhttpserver"
)

const (
	// VLESSCmdTCP represents a VLESS TCP stream command.
	VLESSCmdTCP byte = 0x01
	// VLESSCmdUDP represents a VLESS UDP stream command.
	VLESSCmdUDP byte = 0x02
	// VLESSCmdMux represents a VLESS XUDP/Mux stream command.
	VLESSCmdMux byte = 0x03

	// VLESSAddrIPv4 represents a 4-byte IPv4 VLESS address.
	VLESSAddrIPv4 byte = 0x01
	// VLESSAddrDomain represents a length-prefixed domain VLESS address.
	VLESSAddrDomain byte = 0x02
	// VLESSAddrIPv6 represents a 16-byte IPv6 VLESS address.
	VLESSAddrIPv6 byte = 0x03

	muxOptData byte = 0x01
	muxOptErr  byte = 0x02

	muxStatusNew       byte = 0x01
	muxStatusKeep      byte = 0x02
	muxStatusEnd       byte = 0x03
	muxStatusKeepAlive byte = 0x04

	muxNetTCP byte = 0x01
	muxNetUDP byte = 0x02
)

var (
	// ErrInvalidVLESSVersion indicates an unsupported VLESS protocol version byte.
	ErrInvalidVLESSVersion = errors.New("realityserver: unsupported vless version")
	// ErrUnauthorizedUUID indicates an unknown VLESS client UUID.
	ErrUnauthorizedUUID = errors.New("realityserver: unauthorized client uuid")
	// ErrUnsupportedCommand indicates an unsupported VLESS command byte.
	ErrUnsupportedCommand = errors.New("realityserver: unsupported vless command")
	// ErrInvalidAddressType indicates an unsupported VLESS address type byte.
	ErrInvalidAddressType = errors.New("realityserver: invalid vless address type")
)

type vlessRequest struct {
	Version byte
	UUID    [16]byte
	Command byte
	Host    string
	Port    uint16
}

func readVLESSHeader(r io.Reader, allowed map[[16]byte]struct{}) (*vlessRequest, error) {
	if len(allowed) == 0 {
		return nil, xhttpserver.ErrNoValidClients
	}
	var hdr [18]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, fmt.Errorf("read vless header prefix: %w", err)
	}
	if hdr[0] != 0x00 {
		return nil, fmt.Errorf("%w: %d", ErrInvalidVLESSVersion, hdr[0])
	}
	var uuid [16]byte
	copy(uuid[:], hdr[1:17])
	if _, ok := allowed[uuid]; !ok {
		return nil, ErrUnauthorizedUUID
	}
	addonLen := int(hdr[17])
	if addonLen > 0 {
		if _, err := io.CopyN(io.Discard, r, int64(addonLen)); err != nil {
			return nil, fmt.Errorf("read vless addons: %w", err)
		}
	}

	var cmdBuf [1]byte
	if _, err := io.ReadFull(r, cmdBuf[:]); err != nil {
		return nil, fmt.Errorf("read vless command: %w", err)
	}
	cmd := cmdBuf[0]
	if cmd == VLESSCmdMux {
		return &vlessRequest{
			Version: 0x00,
			UUID:    uuid,
			Command: cmd,
		}, nil
	}
	if cmd != VLESSCmdTCP && cmd != VLESSCmdUDP {
		return nil, fmt.Errorf("%w: %d", ErrUnsupportedCommand, cmd)
	}

	var portAt [3]byte
	if _, err := io.ReadFull(r, portAt[:]); err != nil {
		return nil, fmt.Errorf("read vless port/atype: %w", err)
	}
	port := binary.BigEndian.Uint16(portAt[0:2])
	atype := portAt[2]

	var host string
	switch atype {
	case VLESSAddrIPv4:
		var ip [4]byte
		if _, err := io.ReadFull(r, ip[:]); err != nil {
			return nil, fmt.Errorf("read vless ipv4: %w", err)
		}
		host = net.IP(ip[:]).String()
	case VLESSAddrIPv6:
		var ip [16]byte
		if _, err := io.ReadFull(r, ip[:]); err != nil {
			return nil, fmt.Errorf("read vless ipv6: %w", err)
		}
		host = net.IP(ip[:]).String()
	case VLESSAddrDomain:
		var lBuf [1]byte
		if _, err := io.ReadFull(r, lBuf[:]); err != nil {
			return nil, fmt.Errorf("read vless domain len: %w", err)
		}
		dLen := int(lBuf[0])
		if dLen == 0 {
			return nil, fmt.Errorf("%w: empty domain", ErrInvalidAddressType)
		}
		dBuf := make([]byte, dLen)
		if _, err := io.ReadFull(r, dBuf); err != nil {
			return nil, fmt.Errorf("read vless domain: %w", err)
		}
		host = string(dBuf)
	default:
		return nil, fmt.Errorf("%w: %d", ErrInvalidAddressType, atype)
	}

	return &vlessRequest{
		Version: 0x00,
		UUID:    uuid,
		Command: cmd,
		Host:    host,
		Port:    port,
	}, nil
}

func writeVLESSResponseHeader(w io.Writer) error {
	_, err := w.Write([]byte{0x00, 0x00})
	return err
}

// rawConnTracker tracks accepted raw TCP/TLS connections so Stop can close active ones cleanly.
type rawConnTracker struct {
	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

func newRawConnTracker() *rawConnTracker {
	return &rawConnTracker{conns: make(map[net.Conn]struct{})}
}

func (t *rawConnTracker) Add(c net.Conn) {
	if c == nil {
		return
	}
	t.mu.Lock()
	t.conns[c] = struct{}{}
	t.mu.Unlock()
}

func (t *rawConnTracker) Remove(c net.Conn) {
	if c == nil {
		return
	}
	t.mu.Lock()
	delete(t.conns, c)
	t.mu.Unlock()
}

func (t *rawConnTracker) CloseAll() {
	t.mu.Lock()
	snapshot := make([]net.Conn, 0, len(t.conns))
	for c := range t.conns {
		snapshot = append(snapshot, c)
	}
	t.mu.Unlock()
	for _, c := range snapshot {
		_ = c.Close()
	}
}

func (t *rawConnTracker) Count() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	n := len(t.conns)
	t.mu.Unlock()
	return n
}

type activeStreamEntry struct {
	uuid   [16]byte
	cancel context.CancelFunc
}

// backendRuntime serves VLESS TCP/UDP/Mux and XHTTP stream-one over REALITY listeners.
type backendRuntime struct {
	mu      sync.RWMutex
	allowed map[[16]byte]struct{}
	egress  xhttpserver.EgressConfig
	path    string
	cfgErr  error

	srvMu   sync.Mutex
	httpSrv *http.Server

	sessMu          sync.Mutex
	activeStreamSeq uint64
	activeStreams   map[uint64]activeStreamEntry
}

func newBackendRuntime(cfg xhttpserver.Config) *backendRuntime {
	rt := &backendRuntime{
		activeStreams: make(map[uint64]activeStreamEntry),
	}
	_ = rt.updateConfig(cfg)
	return rt
}

func normalizePathPrefix(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return DefaultXHTTPPath
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if len(p) > 1 {
		p = strings.TrimRight(p, "/")
	}
	return p
}

func validateEgressMode(mode string) error {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "router", "direct", "socks", "interface", "outbound":
		return nil
	default:
		return fmt.Errorf("%w: %q", xhttpserver.ErrUnsupportedEgress, mode)
	}
}

func (rt *backendRuntime) updateConfig(cfg xhttpserver.Config) error {
	allowed := make(map[[16]byte]struct{})
	for _, cid := range cfg.Clients {
		cid = strings.TrimSpace(cid)
		if cid == "" {
			continue
		}
		u, err := xhttpserver.ParseUUID(cid)
		if err != nil {
			rt.mu.Lock()
			rt.cfgErr = err
			rt.mu.Unlock()
			return err
		}
		allowed[u] = struct{}{}
	}
	if len(allowed) == 0 {
		rt.mu.Lock()
		rt.cfgErr = xhttpserver.ErrNoValidClients
		rt.mu.Unlock()
		return xhttpserver.ErrNoValidClients
	}
	if err := validateEgressMode(cfg.Egress.Mode); err != nil {
		rt.mu.Lock()
		rt.cfgErr = err
		rt.mu.Unlock()
		return err
	}

	path := normalizePathPrefix(cfg.PathPrefix)
	egress := cfg.Egress

	rt.mu.Lock()
	rt.allowed = allowed
	rt.egress = egress
	rt.path = path
	rt.cfgErr = nil
	rt.mu.Unlock()

	rt.revokeDisallowedStreams(allowed)
	return nil
}

func (rt *backendRuntime) snapshot() (map[[16]byte]struct{}, xhttpserver.EgressConfig, string, error) {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return rt.allowed, rt.egress, rt.path, rt.cfgErr
}

func (rt *backendRuntime) registerActiveStream(u [16]byte, cancel context.CancelFunc) uint64 {
	rt.sessMu.Lock()
	rt.activeStreamSeq++
	id := rt.activeStreamSeq
	rt.activeStreams[id] = activeStreamEntry{uuid: u, cancel: cancel}
	rt.sessMu.Unlock()
	return id
}

func (rt *backendRuntime) unregisterActiveStream(id uint64) {
	rt.sessMu.Lock()
	delete(rt.activeStreams, id)
	rt.sessMu.Unlock()
}

func (rt *backendRuntime) revokeDisallowedStreams(allowed map[[16]byte]struct{}) {
	var toCancel []context.CancelFunc
	rt.sessMu.Lock()
	for id, entry := range rt.activeStreams {
		if _, ok := allowed[entry.uuid]; !ok {
			toCancel = append(toCancel, entry.cancel)
			delete(rt.activeStreams, id)
		}
	}
	rt.sessMu.Unlock()
	for _, cancel := range toCancel {
		if cancel != nil {
			cancel()
		}
	}
}

func (rt *backendRuntime) closeAllActiveStreams() {
	var toCancel []context.CancelFunc
	rt.sessMu.Lock()
	for id, entry := range rt.activeStreams {
		toCancel = append(toCancel, entry.cancel)
		delete(rt.activeStreams, id)
	}
	rt.sessMu.Unlock()
	for _, cancel := range toCancel {
		if cancel != nil {
			cancel()
		}
	}
}

// serveListener starts HTTP/1.1 + Unencrypted HTTP/2 (H2C) XHTTP stream-one over ln asynchronously.
func (rt *backendRuntime) serveListener(ln net.Listener) error {
	if ln == nil {
		return errors.New("realityserver: nil listener")
	}
	allowed, _, _, cfgErr := rt.snapshot()
	if cfgErr != nil {
		return cfgErr
	}
	if len(allowed) == 0 {
		return xhttpserver.ErrNoValidClients
	}

	var protos http.Protocols
	protos.SetHTTP1(true)
	protos.SetUnencryptedHTTP2(true)

	srv := &http.Server{
		Handler:           http.HandlerFunc(rt.serveHTTP),
		Protocols:         &protos,
		ReadHeaderTimeout: 15 * time.Second,
		MaxHeaderBytes:    16 << 10,
		IdleTimeout:       120 * time.Second,
	}

	rt.srvMu.Lock()
	rt.httpSrv = srv
	rt.srvMu.Unlock()

	go func() {
		_ = srv.Serve(ln)
	}()
	return nil
}

func (rt *backendRuntime) stop() {
	rt.closeAllActiveStreams()
	rt.srvMu.Lock()
	srv := rt.httpSrv
	rt.httpSrv = nil
	rt.srvMu.Unlock()
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		_ = srv.Shutdown(ctx)
		cancel()
		_ = srv.Close()
	}
}

func (rt *backendRuntime) serveVLESSConn(ctx context.Context, conn net.Conn, tracker *rawConnTracker) {
	if tracker != nil {
		tracker.Add(conn)
		defer tracker.Remove(conn)
	}
	defer func() { _ = conn.Close() }()

	if ctx == nil {
		ctx = context.Background()
	}
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	stopWatch := context.AfterFunc(streamCtx, func() {
		_ = conn.SetDeadline(time.Now())
		_ = conn.Close()
	})
	defer stopWatch()

	allowed, egress, _, cfgErr := rt.snapshot()
	if cfgErr != nil || len(allowed) == 0 {
		return
	}

	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	req, err := readVLESSHeader(conn, allowed)
	if err != nil {
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	activeID := rt.registerActiveStream(req.UUID, cancel)
	defer rt.unregisterActiveStream(activeID)

	if err := writeVLESSResponseHeader(conn); err != nil {
		return
	}

	rt.handleVLESSPreParsed(streamCtx, egress, req, conn, conn)
}

type flushWriter struct {
	mu      *sync.Mutex
	w       io.Writer
	flusher http.Flusher
}

func (fw *flushWriter) Write(p []byte) (int, error) {
	if fw.mu != nil {
		fw.mu.Lock()
		defer fw.mu.Unlock()
	}
	n, err := fw.w.Write(p)
	if err == nil && fw.flusher != nil {
		fw.flusher.Flush()
	}
	return n, err
}

func randomPaddingString() string {
	var lenBuf [1]byte
	_, _ = rand.Read(lenBuf[:])
	n := 32 + int(lenBuf[0]%64)
	raw := make([]byte, n)
	_, _ = rand.Read(raw)
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	out := make([]byte, n)
	for i, b := range raw {
		out[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(out)
}

func (rt *backendRuntime) serveHTTP(w http.ResponseWriter, r *http.Request) {
	allowed, egress, pathPrefix, cfgErr := rt.snapshot()
	if cfgErr != nil || len(allowed) == 0 {
		http.Error(w, "server not configured", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	reqPath := r.URL.Path
	if reqPath != pathPrefix && !strings.HasPrefix(reqPath, pathPrefix+"/") {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if tail := strings.Trim(strings.TrimPrefix(reqPath, pathPrefix), "/"); tail != "" {
		// Inside XHTTP stream-one preset only root path stream-one is needed.
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	rc := http.NewResponseController(w)
	_ = rc.EnableFullDuplex()

	req, err := readVLESSHeader(r.Body, allowed)
	if err != nil {
		_ = r.Body.Close()
		if errors.Is(err, ErrUnauthorizedUUID) || errors.Is(err, xhttpserver.ErrNoValidClients) || errors.Is(err, xhttpserver.ErrInvalidClientUUID) {
			http.Error(w, "forbidden", http.StatusForbidden)
		} else {
			http.Error(w, "invalid vless header", http.StatusBadRequest)
		}
		return
	}

	streamCtx, cancel := context.WithCancel(r.Context())
	defer cancel()

	var wMu sync.Mutex
	var handlerDone atomic.Bool

	activeID := rt.registerActiveStream(req.UUID, cancel)
	defer rt.unregisterActiveStream(activeID)

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Padding", randomPaddingString())
	w.WriteHeader(http.StatusOK)

	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}

	fw := &flushWriter{mu: &wMu, w: w, flusher: flusher}
	wMu.Lock()
	respErr := writeVLESSResponseHeader(w)
	if respErr == nil && flusher != nil {
		flusher.Flush()
	}
	wMu.Unlock()
	if respErr != nil {
		_ = r.Body.Close()
		return
	}

	afterDone := make(chan struct{})
	stopWatch := context.AfterFunc(streamCtx, func() {
		defer close(afterDone)
		wMu.Lock()
		if !handlerDone.Load() {
			_ = http.NewResponseController(w).SetReadDeadline(time.Now())
		}
		wMu.Unlock()
		_ = r.Body.Close()
	})
	defer func() {
		wMu.Lock()
		handlerDone.Store(true)
		wMu.Unlock()
		if !stopWatch() {
			<-afterDone
		}
	}()

	rt.handleVLESSPreParsed(streamCtx, egress, req, r.Body, fw)
	_ = r.Body.Close()
}

func (rt *backendRuntime) handleVLESSPreParsed(
	ctx context.Context,
	egress xhttpserver.EgressConfig,
	req *vlessRequest,
	up io.Reader,
	down io.Writer,
) {
	if req == nil {
		return
	}
	if req.Command == VLESSCmdMux {
		rt.handleVLESSMux(ctx, egress, up, down)
		return
	}
	if req.Command == VLESSCmdUDP {
		rt.handleVLESSUDP(ctx, egress, req, up, down)
		return
	}
	if req.Command != VLESSCmdTCP {
		return
	}

	targetHost := req.Host
	var initialPayload []byte
	var initialErr error

	// Sniff TLS SNI / HTTP Host on raw IP targets to preserve domain routing / ACL accuracy.
	if net.ParseIP(targetHost) != nil {
		initialPayload, initialErr = readInitialPayloadForSniff(up, req.Port)
		if len(initialPayload) > 0 {
			if sni := sniffTLSSNI(initialPayload); sni != "" && net.ParseIP(sni) == nil {
				targetHost = sni
			} else if httpHost := sniffHTTPHost(initialPayload); httpHost != "" && net.ParseIP(httpHost) == nil {
				targetHost = httpHost
			}
		}
		if len(initialPayload) == 0 && initialErr != nil {
			return
		}
	}

	dialCtx, dialCancel := context.WithTimeout(ctx, 15*time.Second)
	targetConn, err := dialTCP(dialCtx, egress, targetHost, req.Port)
	dialCancel()
	if err != nil {
		return
	}
	defer func() { _ = targetConn.Close() }()

	if len(initialPayload) > 0 {
		if _, werr := targetConn.Write(initialPayload); werr != nil {
			return
		}
	}
	if initialErr != nil {
		if errors.Is(initialErr, io.EOF) {
			if tc, ok := targetConn.(interface{ CloseWrite() error }); ok {
				_ = tc.CloseWrite()
			}
			_, _ = io.Copy(down, targetConn)
		}
		return
	}

	done := make(chan struct{}, 2)
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 32*1024)
		for {
			n, rerr := up.Read(buf)
			if n > 0 {
				if _, werr := targetConn.Write(buf[:n]); werr != nil {
					break
				}
			}
			if rerr != nil {
				if tc, ok := targetConn.(interface{ CloseWrite() error }); ok {
					_ = tc.CloseWrite()
				}
				break
			}
		}
	}()

	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 32*1024)
		for {
			n, rerr := targetConn.Read(buf)
			if n > 0 {
				if _, werr := down.Write(buf[:n]); werr != nil {
					break
				}
			}
			if rerr != nil {
				break
			}
		}
	}()

	select {
	case <-done:
	case <-ctx.Done():
	}
	_ = targetConn.Close()
}

func (rt *backendRuntime) handleVLESSUDP(
	ctx context.Context,
	egress xhttpserver.EgressConfig,
	req *vlessRequest,
	up io.Reader,
	down io.Writer,
) {
	dialCtx, dialCancel := context.WithTimeout(ctx, 15*time.Second)
	udpConn, err := dialUDP(dialCtx, egress, req.Host, req.Port)
	dialCancel()
	if err != nil {
		return
	}
	defer func() { _ = udpConn.Close() }()

	done := make(chan struct{}, 2)
	go func() {
		defer func() { done <- struct{}{} }()
		var lenBuf [2]byte
		pktBuf := make([]byte, 65536)
		for {
			if _, err := io.ReadFull(up, lenBuf[:]); err != nil {
				break
			}
			pktLen := int(binary.BigEndian.Uint16(lenBuf[:]))
			if pktLen == 0 {
				continue
			}
			if _, err := io.ReadFull(up, pktBuf[:pktLen]); err != nil {
				break
			}
			if _, werr := udpConn.Write(pktBuf[:pktLen]); werr != nil {
				break
			}
		}
	}()

	go func() {
		defer func() { done <- struct{}{} }()
		readBuf := make([]byte, 65536)
		outBuf := make([]byte, 2+65536)
		for {
			n, rerr := udpConn.Read(readBuf)
			if n > 0 {
				binary.BigEndian.PutUint16(outBuf[:2], uint16(n))
				copy(outBuf[2:2+n], readBuf[:n])
				if _, werr := down.Write(outBuf[:2+n]); werr != nil {
					break
				}
			}
			if rerr != nil {
				break
			}
		}
	}()

	select {
	case <-done:
	case <-ctx.Done():
	}
	_ = udpConn.Close()
}

type muxSubSession struct {
	id         uint16
	netType    byte
	targetHost string
	targetPort uint16
	conn       net.Conn
}

func (rt *backendRuntime) handleVLESSMux(
	ctx context.Context,
	egress xhttpserver.EgressConfig,
	up io.Reader,
	down io.Writer,
) {
	var downMu sync.Mutex
	writeDown := func(b []byte) error {
		downMu.Lock()
		defer downMu.Unlock()
		_, err := down.Write(b)
		return err
	}

	var subsMu sync.Mutex
	subs := make(map[uint16]*muxSubSession)
	closeAllSubs := func() {
		subsMu.Lock()
		for id, sub := range subs {
			_ = sub.conn.Close()
			delete(subs, id)
		}
		subsMu.Unlock()
	}
	defer closeAllSubs()

	var hdrLenBuf [2]byte
	for {
		if _, err := io.ReadFull(up, hdrLenBuf[:]); err != nil {
			return
		}
		metaLen := int(binary.BigEndian.Uint16(hdrLenBuf[:]))
		if metaLen < 4 || metaLen > 1024 {
			return
		}
		metaBuf := make([]byte, metaLen)
		if _, err := io.ReadFull(up, metaBuf); err != nil {
			return
		}
		sessID := binary.BigEndian.Uint16(metaBuf[0:2])
		status := metaBuf[2]
		opt := metaBuf[3]

		var payload []byte
		if (opt & muxOptData) != 0 {
			var pLenBuf [2]byte
			if _, err := io.ReadFull(up, pLenBuf[:]); err != nil {
				return
			}
			pLen := int(binary.BigEndian.Uint16(pLenBuf[:]))
			if pLen > 0 {
				payload = make([]byte, pLen)
				if _, err := io.ReadFull(up, payload); err != nil {
					return
				}
			}
		}

		switch status {
		case muxStatusNew:
			if metaLen < 7 {
				continue
			}
			netType, host, port, ok := parseMuxTargetAddr(metaBuf[4:])
			if !ok {
				continue
			}
			dialCtx, dialCancel := context.WithTimeout(ctx, 15*time.Second)
			var conn net.Conn
			var dialErr error
			if netType == muxNetUDP {
				conn, dialErr = dialUDP(dialCtx, egress, host, port)
			} else {
				conn, dialErr = dialTCP(dialCtx, egress, host, port)
			}
			dialCancel()
			if dialErr != nil {
				_ = writeDown(encodeMuxFrame(sessID, muxStatusEnd, muxOptErr, false, "", 0, nil))
				continue
			}
			sub := &muxSubSession{id: sessID, netType: netType, targetHost: host, targetPort: port, conn: conn}
			subsMu.Lock()
			if prev, exists := subs[sessID]; exists {
				_ = prev.conn.Close()
			}
			subs[sessID] = sub
			subsMu.Unlock()

			if len(payload) > 0 {
				if _, err := conn.Write(payload); err != nil {
					_ = conn.Close()
					subsMu.Lock()
					delete(subs, sessID)
					subsMu.Unlock()
					_ = writeDown(encodeMuxFrame(sessID, muxStatusEnd, muxOptErr, false, "", 0, nil))
					continue
				}
			}
			go func(s *muxSubSession) {
				buf := make([]byte, 32*1024)
				for {
					n, rerr := s.conn.Read(buf)
					if n > 0 {
						frame := encodeMuxFrame(s.id, muxStatusKeep, muxOptData, s.netType == muxNetUDP, s.targetHost, s.targetPort, buf[:n])
						if werr := writeDown(frame); werr != nil {
							break
						}
					}
					if rerr != nil {
						break
					}
				}
				_ = s.conn.Close()
				subsMu.Lock()
				if cur, ok := subs[s.id]; ok && cur == s {
					delete(subs, s.id)
				}
				subsMu.Unlock()
				_ = writeDown(encodeMuxFrame(s.id, muxStatusEnd, 0, false, "", 0, nil))
			}(sub)

		case muxStatusKeep:
			if len(payload) > 0 {
				subsMu.Lock()
				sub := subs[sessID]
				subsMu.Unlock()
				if sub != nil {
					if _, err := sub.conn.Write(payload); err != nil {
						_ = sub.conn.Close()
						subsMu.Lock()
						delete(subs, sessID)
						subsMu.Unlock()
						_ = writeDown(encodeMuxFrame(sessID, muxStatusEnd, muxOptErr, false, "", 0, nil))
					}
				}
			}

		case muxStatusEnd:
			subsMu.Lock()
			sub := subs[sessID]
			if sub != nil {
				delete(subs, sessID)
			}
			subsMu.Unlock()
			if sub != nil {
				if len(payload) > 0 {
					_, _ = sub.conn.Write(payload)
				}
				_ = sub.conn.Close()
			}
		case muxStatusKeepAlive:
		}
	}
}

func parseMuxTargetAddr(b []byte) (netType byte, host string, port uint16, ok bool) {
	if len(b) < 4 {
		return 0, "", 0, false
	}
	netType = b[0]
	port = binary.BigEndian.Uint16(b[1:3])
	atype := b[3]
	rest := b[4:]
	switch atype {
	case VLESSAddrIPv4:
		if len(rest) < 4 {
			return 0, "", 0, false
		}
		return netType, net.IP(rest[:4]).String(), port, true
	case VLESSAddrIPv6:
		if len(rest) < 16 {
			return 0, "", 0, false
		}
		return netType, net.IP(rest[:16]).String(), port, true
	case VLESSAddrDomain:
		if len(rest) < 1 {
			return 0, "", 0, false
		}
		dLen := int(rest[0])
		if dLen == 0 || len(rest) < 1+dLen {
			return 0, "", 0, false
		}
		return netType, string(rest[1 : 1+dLen]), port, true
	default:
		return 0, "", 0, false
	}
}

func encodeMuxFrame(sessionID uint16, status, opt byte, includeUDPAddr bool, host string, port uint16, payload []byte) []byte {
	var addrBytes []byte
	if includeUDPAddr {
		addrBytes = make([]byte, 0, 22)
		addrBytes = append(addrBytes, muxNetUDP, byte(port>>8), byte(port))
		if ip := net.ParseIP(host); ip != nil {
			if ip4 := ip.To4(); ip4 != nil {
				addrBytes = append(addrBytes, VLESSAddrIPv4)
				addrBytes = append(addrBytes, ip4...)
			} else {
				addrBytes = append(addrBytes, VLESSAddrIPv6)
				addrBytes = append(addrBytes, ip.To16()...)
			}
		} else {
			if len(host) > 255 {
				host = host[:255]
			}
			addrBytes = append(addrBytes, VLESSAddrDomain, byte(len(host)))
			addrBytes = append(addrBytes, host...)
		}
	}
	metaLen := 4 + len(addrBytes)
	totalLen := 2 + metaLen
	if (opt & muxOptData) != 0 {
		totalLen += 2 + len(payload)
	}
	out := make([]byte, totalLen)
	binary.BigEndian.PutUint16(out[0:2], uint16(metaLen))
	binary.BigEndian.PutUint16(out[2:4], sessionID)
	out[4] = status
	out[5] = opt
	if len(addrBytes) > 0 {
		copy(out[6:6+len(addrBytes)], addrBytes)
	}
	if (opt & muxOptData) != 0 {
		pOff := 2 + metaLen
		binary.BigEndian.PutUint16(out[pOff:pOff+2], uint16(len(payload)))
		copy(out[pOff+2:], payload)
	}
	return out
}

func dialTCP(ctx context.Context, cfg xhttpserver.EgressConfig, host string, port uint16) (net.Conn, error) {
	if err := xhttpserver.CheckDestinationACL(host, cfg.AllowPrivateDestinations); err != nil {
		return nil, err
	}
	resolved, err := resolveHost(ctx, cfg, host)
	if err != nil {
		return nil, err
	}
	if err := xhttpserver.CheckDestinationACL(resolved, cfg.AllowPrivateDestinations); err != nil {
		return nil, err
	}

	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	switch mode {
	case "", "router", "direct":
		return (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(resolved, strconv.Itoa(int(port))))
	case "outbound":
		return (&net.Dialer{Timeout: 15 * time.Second, Control: sockMarkControl(1)}).DialContext(ctx, "tcp", net.JoinHostPort(resolved, strconv.Itoa(int(port))))
	case "interface":
		if strings.TrimSpace(cfg.InterfaceName) == "" {
			return nil, fmt.Errorf("realityserver: interface egress requires non-empty InterfaceName")
		}
		return (&net.Dialer{Timeout: 15 * time.Second, Control: bindToDeviceControl(cfg.InterfaceName)}).DialContext(ctx, "tcp", net.JoinHostPort(resolved, strconv.Itoa(int(port))))
	case "socks":
		return dialSOCKS5(ctx, cfg.SocksAddr, resolved, port)
	default:
		return nil, fmt.Errorf("realityserver: unsupported egress mode %q", cfg.Mode)
	}
}

func dialUDP(ctx context.Context, cfg xhttpserver.EgressConfig, host string, port uint16) (net.Conn, error) {
	if err := xhttpserver.CheckDestinationACL(host, cfg.AllowPrivateDestinations); err != nil {
		return nil, err
	}
	resolved, err := resolveHost(ctx, cfg, host)
	if err != nil {
		return nil, err
	}
	if err := xhttpserver.CheckDestinationACL(resolved, cfg.AllowPrivateDestinations); err != nil {
		return nil, err
	}

	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	addr := net.JoinHostPort(resolved, strconv.Itoa(int(port)))
	switch mode {
	case "", "router", "direct", "socks":
		return (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "udp", addr)
	case "outbound":
		return (&net.Dialer{Timeout: 15 * time.Second, Control: sockMarkControl(1)}).DialContext(ctx, "udp", addr)
	case "interface":
		if strings.TrimSpace(cfg.InterfaceName) == "" {
			return nil, fmt.Errorf("realityserver: interface egress requires non-empty InterfaceName")
		}
		return (&net.Dialer{Timeout: 15 * time.Second, Control: bindToDeviceControl(cfg.InterfaceName)}).DialContext(ctx, "udp", addr)
	default:
		return nil, fmt.Errorf("realityserver: unsupported egress mode %q", cfg.Mode)
	}
}

func resolveHost(ctx context.Context, cfg xhttpserver.EgressConfig, host string) (string, error) {
	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), nil
	}
	resolver := net.DefaultResolver
	if dnsAddr := strings.TrimSpace(cfg.DNSProxyAddr); dnsAddr != "" {
		resolver = &net.Resolver{
			PreferGo: true,
			Dial: func(dCtx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(dCtx, network, dnsAddr)
			},
		}
	}
	network := "ip"
	if strings.EqualFold(strings.TrimSpace(cfg.DomainStrategy), "ipv4_only") {
		network = "ip4"
	}
	ips, err := resolver.LookupIP(ctx, network, host)
	if err != nil && cfg.DNSProxyAddr != "" {
		// Fall back to system resolver if local DNS proxy does not respond.
		ips, err = net.DefaultResolver.LookupIP(ctx, network, host)
	}
	if err != nil || len(ips) == 0 {
		return host, err
	}
	preferIPv4 := strings.EqualFold(strings.TrimSpace(cfg.DomainStrategy), "prefer_ipv4") ||
		strings.EqualFold(strings.TrimSpace(cfg.DomainStrategy), "ipv4_only")
	if preferIPv4 {
		for _, ip := range ips {
			if ip4 := ip.To4(); ip4 != nil {
				return ip4.String(), nil
			}
		}
	}
	return ips[0].String(), nil
}

func dialSOCKS5(ctx context.Context, socksAddr, host string, port uint16) (net.Conn, error) {
	if strings.TrimSpace(socksAddr) == "" {
		return nil, errors.New("realityserver: socks egress requires SocksAddr")
	}
	conn, err := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", socksAddr)
	if err != nil {
		return nil, fmt.Errorf("dial socks5 %s: %w", socksAddr, err)
	}
	cleanup := func(e error) (net.Conn, error) {
		_ = conn.Close()
		return nil, e
	}

	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return cleanup(fmt.Errorf("socks5 greeting: %w", err))
	}
	var greetResp [2]byte
	if _, err := io.ReadFull(conn, greetResp[:]); err != nil {
		return cleanup(fmt.Errorf("socks5 greeting reply: %w", err))
	}
	if greetResp[0] != 0x05 || greetResp[1] != 0x00 {
		return cleanup(fmt.Errorf("socks5 auth rejected: %v", greetResp))
	}

	req := make([]byte, 0, 265)
	req = append(req, 0x05, 0x01, 0x00)
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			req = append(req, 0x01)
			req = append(req, ip4...)
		} else {
			req = append(req, 0x04)
			req = append(req, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return cleanup(fmt.Errorf("socks5 hostname too long: %d", len(host)))
		}
		req = append(req, 0x03, byte(len(host)))
		req = append(req, host...)
	}
	req = append(req, byte(port>>8), byte(port))
	if _, err := conn.Write(req); err != nil {
		return cleanup(fmt.Errorf("socks5 connect write: %w", err))
	}

	var respHdr [4]byte
	if _, err := io.ReadFull(conn, respHdr[:]); err != nil {
		return cleanup(fmt.Errorf("socks5 connect reply: %w", err))
	}
	if respHdr[1] != 0x00 {
		return cleanup(fmt.Errorf("socks5 connect failed code=%d", respHdr[1]))
	}
	switch respHdr[3] {
	case 0x01:
		var bnd [6]byte
		if _, err := io.ReadFull(conn, bnd[:]); err != nil {
			return cleanup(err)
		}
	case 0x04:
		var bnd [18]byte
		if _, err := io.ReadFull(conn, bnd[:]); err != nil {
			return cleanup(err)
		}
	case 0x03:
		var lBuf [1]byte
		if _, err := io.ReadFull(conn, lBuf[:]); err != nil {
			return cleanup(err)
		}
		bnd := make([]byte, int(lBuf[0])+2)
		if _, err := io.ReadFull(conn, bnd); err != nil {
			return cleanup(err)
		}
	}
	return conn, nil
}

func readInitialPayloadForSniff(up io.Reader, port uint16) ([]byte, error) {
	if dr, ok := up.(interface{ SetReadDeadline(time.Time) error }); ok && port != 443 && port != 80 {
		_ = dr.SetReadDeadline(time.Now().Add(25 * time.Millisecond))
		defer func() { _ = dr.SetReadDeadline(time.Time{}) }()
	}
	buf := make([]byte, 4096)
	total := 0
	for total < len(buf) {
		n, err := up.Read(buf[total:])
		if n > 0 {
			total += n
		}
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() && total == 0 {
				return nil, nil
			}
			return buf[:total], err
		}
		if total > 0 && !isIncompleteTLSClientHello(buf[:total]) && !isIncompleteHTTPHeader(buf[:total]) {
			return buf[:total], nil
		}
	}
	return buf[:total], nil
}

func isIncompleteTLSClientHello(b []byte) bool {
	if len(b) < 5 || b[0] != 0x16 {
		return false
	}
	recLen := int(binary.BigEndian.Uint16(b[3:5]))
	return len(b) < 5+recLen
}

func isIncompleteHTTPHeader(b []byte) bool {
	if len(b) == 0 || bytes.Index(b, []byte("\r\n\r\n")) >= 0 || bytes.Index(b, []byte("\n\n")) >= 0 {
		return false
	}
	prefixes := []string{"GET ", "POST ", "PUT ", "HEAD ", "DELETE ", "OPTIONS ", "PATCH ", "CONNECT "}
	upper := strings.ToUpper(string(b[:min(len(b), 16)]))
	for _, p := range prefixes {
		if strings.HasPrefix(upper, p) || strings.HasPrefix(p, upper) {
			return true
		}
	}
	return false
}

func sniffTLSSNI(b []byte) string {
	if len(b) < 43 || b[0] != 0x16 {
		return ""
	}
	recLen := int(binary.BigEndian.Uint16(b[3:5]))
	if len(b) < 5+recLen || b[5] != 0x01 {
		return ""
	}
	pos := 5 + 4 + 2 + 32
	if pos >= len(b) {
		return ""
	}
	sidLen := int(b[pos])
	pos += 1 + sidLen
	if pos+2 > len(b) {
		return ""
	}
	csLen := int(binary.BigEndian.Uint16(b[pos : pos+2]))
	pos += 2 + csLen
	if pos >= len(b) {
		return ""
	}
	compLen := int(b[pos])
	pos += 1 + compLen
	if pos+2 > len(b) {
		return ""
	}
	extTotal := int(binary.BigEndian.Uint16(b[pos : pos+2]))
	pos += 2
	end := min(len(b), pos+extTotal)
	for pos+4 <= end {
		extType := binary.BigEndian.Uint16(b[pos : pos+2])
		extLen := int(binary.BigEndian.Uint16(b[pos+2 : pos+4]))
		pos += 4
		if pos+extLen > end {
			break
		}
		if extType == 0x0000 && extLen >= 5 {
			listLen := int(binary.BigEndian.Uint16(b[pos : pos+2]))
			if listLen >= 3 && 2+listLen <= extLen && b[pos+2] == 0x00 {
				nameLen := int(binary.BigEndian.Uint16(b[pos+3 : pos+5]))
				if 5+nameLen <= extLen {
					host := string(b[pos+5 : pos+5+nameLen])
					if isValidSniffedDomain(host) {
						return host
					}
				}
			}
		}
		pos += extLen
	}
	return ""
}

func sniffHTTPHost(b []byte) string {
	idx := bytes.Index(b, []byte("\r\n\r\n"))
	if idx < 0 {
		return ""
	}
	sc := bufio.NewScanner(bytes.NewReader(b[:idx]))
	if !sc.Scan() {
		return ""
	}
	for sc.Scan() {
		line := sc.Text()
		if len(line) > 5 && strings.EqualFold(line[:5], "Host:") {
			raw := strings.TrimSpace(line[5:])
			if h, _, err := net.SplitHostPort(raw); err == nil {
				raw = h
			}
			if isValidSniffedDomain(raw) {
				return raw
			}
			return ""
		}
	}
	return ""
}

func isValidSniffedDomain(h string) bool {
	if h == "" || len(h) > 253 || net.ParseIP(h) != nil {
		return false
	}
	return !strings.ContainsAny(h, " \t\r\n/\\")
}
