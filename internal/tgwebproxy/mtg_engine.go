package tgwebproxy

import (
	"context"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/9seconds/mtg/v2/antireplay"
	"github.com/9seconds/mtg/v2/essentials"
	"github.com/9seconds/mtg/v2/events"
	"github.com/9seconds/mtg/v2/ipblocklist"
	"github.com/9seconds/mtg/v2/logger"
	"github.com/9seconds/mtg/v2/mtglib"

	mtgctr "github.com/hoaxisr/awg-manager/internal/tgwebproxy/ctr"
)

type mtgAllowAll struct{}

func (mtgAllowAll) Contains(net.IP) bool { return true }
func (mtgAllowAll) Run(time.Duration)    {}
func (mtgAllowAll) Shutdown()            {}

type mtgActiveSpec struct {
	Secret        string
	LegacySecret  string
	AllSecretsCSV string
	TlsDomain     string
	UpstreamsKey  string
	DirectPort    int
	RawAddr       string
	DirectEnabled bool
	RawEnabled    bool
}

// MtgEngine manages the embedded Go mtg (MTProto Fake-TLS + raw obfuscated2) listeners in-process,
// replacing the external telemt-direct and telemt-raw binaries.
type MtgEngine struct {
	mu        sync.Mutex
	proxy     *mtglib.Proxy
	evStream  *events.EventStream
	secrets   []mtglib.Secret
	directLis net.Listener
	rawLis    net.Listener
	active    mtgActiveSpec
	lastCfg   Config
	hasCfg    bool
	connected atomic.Int64
}

func NewMtgEngine() *MtgEngine {
	return &MtgEngine{}
}

type mtgConnObserver struct {
	events.Observer
	connected *atomic.Int64
	active    map[string]struct{}
}

func (o *mtgConnObserver) EventConnectedToDC(evt mtglib.EventConnectedToDC) {
	if o.active == nil {
		o.active = make(map[string]struct{})
	}
	if _, exists := o.active[evt.StreamID()]; !exists {
		o.active[evt.StreamID()] = struct{}{}
		o.connected.Add(1)
	}
}

func (o *mtgConnObserver) EventFinish(evt mtglib.EventFinish) {
	if o.active == nil {
		return
	}
	if _, exists := o.active[evt.StreamID()]; exists {
		delete(o.active, evt.StreamID())
		if v := o.connected.Add(-1); v < 0 {
			o.connected.Store(0)
		}
	}
}

func (o *mtgConnObserver) Shutdown() {
	if n := len(o.active); n > 0 {
		o.active = nil
		if v := o.connected.Add(-int64(n)); v < 0 {
			o.connected.Store(0)
		}
	}
}

func parseMtgSecret(hexKey string, tlsDomain string) (mtglib.Secret, error) {
	cleanHex := strings.TrimSpace(strings.ToLower(hexKey))
	keyBytes, err := hex.DecodeString(cleanHex)
	if err != nil || len(keyBytes) != mtglib.SecretKeyLength {
		return mtglib.Secret{}, fmt.Errorf("invalid 16-byte hex secret: %q", hexKey)
	}
	host := strings.TrimSpace(strings.ToLower(tlsDomain))
	if host == "" {
		host = "ya.ru"
	}
	var sec mtglib.Secret
	copy(sec.Key[:], keyBytes)
	sec.Host = host
	if !sec.Valid() {
		return mtglib.Secret{}, fmt.Errorf("constructed mtg secret is invalid for host %q", host)
	}
	return sec, nil
}

func buildMtgSpec(cfg Config) mtgActiveSpec {
	directPort := cfg.DirectPort
	if directPort <= 0 {
		directPort = DefaultDirectPort
	}
	rawAddr := strings.TrimSpace(cfg.Backend)
	if rawAddr == "" {
		rawAddr = DefaultBackend
	}
	tlsDomain := strings.TrimSpace(strings.ToLower(cfg.TlsDomain))
	if tlsDomain == "" {
		tlsDomain = "ya.ru"
	}
	return mtgActiveSpec{
		Secret:        strings.TrimSpace(strings.ToLower(cfg.Secret)),
		LegacySecret:  strings.TrimSpace(strings.ToLower(cfg.LegacySecret)),
		AllSecretsCSV: strings.Join(cfg.ActiveSecrets(), ","),
		TlsDomain:     tlsDomain,
		UpstreamsKey:  upstreamPoolSignature(cfg.EnabledUpstreams()),
		DirectPort:    directPort,
		RawAddr:       rawAddr,
		DirectEnabled: cfg.Enabled && cfg.IsDirectEnabled(),
		RawEnabled:    cfg.Enabled && cfg.IsRawEnabled(),
	}
}

func (e *MtgEngine) Apply(cfg Config) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.lastCfg = cfg
	e.hasCfg = true

	spec := buildMtgSpec(cfg)
	if !spec.DirectEnabled && !spec.RawEnabled {
		e.stopLocked()
		return nil
	}

	if e.proxy != nil &&
		e.active == spec &&
		(spec.DirectEnabled == (e.directLis != nil)) &&
		(spec.RawEnabled == (e.rawLis != nil)) {
		return nil
	}

	e.stopLocked()
	return e.startLocked(spec, cfg.EnabledUpstreams())
}

func (e *MtgEngine) Restart() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.hasCfg {
		return nil
	}
	spec := buildMtgSpec(e.lastCfg)
	e.stopLocked()
	if !spec.DirectEnabled && !spec.RawEnabled {
		return nil
	}
	return e.startLocked(spec, e.lastCfg.EnabledUpstreams())
}

func (e *MtgEngine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.stopLocked()
}

func (e *MtgEngine) IsDirectRunning() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.proxy != nil && e.directLis != nil
}

func (e *MtgEngine) IsRawRunning() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.proxy != nil && e.rawLis != nil
}

func (e *MtgEngine) ConnectedCount() int64 {
	return e.connected.Load()
}

func (e *MtgEngine) startLocked(spec mtgActiveSpec, upstreams []UpstreamSpec) error {
	var secrets []mtglib.Secret
	if spec.AllSecretsCSV != "" {
		for _, hexSec := range strings.Split(spec.AllSecretsCSV, ",") {
			if sec, err := parseMtgSecret(hexSec, spec.TlsDomain); err == nil {
				secrets = append(secrets, sec)
			}
		}
	}
	if len(secrets) == 0 && spec.Secret != "" {
		sec0, err := parseMtgSecret(spec.Secret, spec.TlsDomain)
		if err != nil {
			return err
		}
		secrets = append(secrets, sec0)
		if spec.LegacySecret != "" && spec.LegacySecret != spec.Secret {
			if sec1, err := parseMtgSecret(spec.LegacySecret, spec.TlsDomain); err == nil {
				secrets = append(secrets, sec1)
			}
		}
	}
	if len(secrets) == 0 {
		dummyHex, err := randomHexSecret(rand.Reader)
		if err != nil {
			return err
		}
		secDummy, err := parseMtgSecret(dummyHex, spec.TlsDomain)
		if err != nil {
			return err
		}
		secrets = append(secrets, secDummy)
	}

	ntw := newMtgUpstreamPool(upstreams)

	e.connected.Store(0)
	evStream := events.NewEventStream([]events.ObserverFactory{
		func() events.Observer {
			return &mtgConnObserver{
				Observer:  events.NewNoopObserver(),
				connected: &e.connected,
			}
		},
	})

	proxy, err := mtglib.NewProxy(mtglib.ProxyOpts{
		Secrets:                  secrets,
		Network:                  ntw,
		AntiReplayCache:          antireplay.NewStableBloomFilter(antireplay.DefaultStableBloomFilterMaxSize, antireplay.DefaultStableBloomFilterErrorRate),
		IPBlocklist:              ipblocklist.NewNoop(),
		IPAllowlist:              mtgAllowAll{},
		EventStream:              evStream,
		Logger:                   logger.NewNoopLogger(),
		PreferIP:                 "prefer-ipv4",
		AllowFallbackOnUnknownDC: true,
		TolerateTimeSkewness:     5 * time.Minute,
	})
	if err != nil {
		evStream.Shutdown()
		return fmt.Errorf("create mtg proxy: %w", err)
	}

	var directLis net.Listener
	if spec.DirectEnabled {
		directAddr := fmt.Sprintf("0.0.0.0:%d", spec.DirectPort)
		directLis, err = net.Listen("tcp", directAddr)
		if err != nil {
			proxy.Shutdown()
			evStream.Shutdown()
			return fmt.Errorf("listen mtg direct on %s: %w", directAddr, err)
		}
	}

	var rawLis net.Listener
	if spec.RawEnabled {
		rawLis, err = net.Listen("tcp", spec.RawAddr)
		if err != nil {
			if directLis != nil {
				_ = directLis.Close()
			}
			proxy.Shutdown()
			evStream.Shutdown()
			return fmt.Errorf("listen mtg raw on %s: %w", spec.RawAddr, err)
		}
	}

	e.proxy = proxy
	e.evStream = &evStream
	e.secrets = secrets
	e.directLis = directLis
	e.rawLis = rawLis
	e.active = spec

	if directLis != nil {
		go func(p *mtglib.Proxy, l net.Listener) {
			_ = p.Serve(l)
		}(proxy, directLis)
	}

	if rawLis != nil {
		go e.serveRawLoop(rawLis, proxy, ntw, secrets)
	}

	return nil
}

func (e *MtgEngine) stopLocked() {
	if e.directLis != nil {
		_ = e.directLis.Close()
		e.directLis = nil
	}
	if e.rawLis != nil {
		_ = e.rawLis.Close()
		e.rawLis = nil
	}
	if e.proxy != nil {
		e.proxy.Shutdown()
		e.proxy = nil
	}
	if e.evStream != nil {
		e.evStream.Shutdown()
		e.evStream = nil
	}
	e.secrets = nil
	e.active = mtgActiveSpec{}
	e.connected.Store(0)
}

func (e *MtgEngine) serveRawLoop(lis net.Listener, proxy *mtglib.Proxy, ntw mtglib.Network, secrets []mtglib.Secret) {
	for {
		conn, err := lis.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			return
		}
		go e.handleRawConn(conn, proxy, ntw, secrets)
	}
}

var (
	mtgConnectionTypeSecure       = [4]byte{0xdd, 0xdd, 0xdd, 0xdd}
	mtgConnectionTypeIntermediate = [4]byte{0xee, 0xee, 0xee, 0xee}
	mtgConnectionTypeAbridged     = [4]byte{0xef, 0xef, 0xef, 0xef}
)

type parsedObfuscated2 struct {
	secretIdx  int
	connType   [4]byte
	dcBytes    [2]byte
	dc         int
	recvCipher cipher.Stream
	sendCipher cipher.Stream
	matched    bool
}

func parseObfuscated2Frame(frame []byte, secrets []mtglib.Secret) parsedObfuscated2 {
	if len(frame) < 64 {
		return parsedObfuscated2{}
	}
	for idx, sec := range secrets {
		h := sha256.New()
		h.Write(frame[8:40])
		h.Write(sec.Key[:])
		recvKey := h.Sum(nil)

		recvCipher, err := mtgctr.New(recvKey, frame[40:56])
		if err != nil {
			continue
		}
		var dec [64]byte
		recvCipher.XORKeyStream(dec[:], frame[:64])

		var connType [4]byte
		copy(connType[:], dec[56:60])
		isSecure := subtle.ConstantTimeCompare(connType[:], mtgConnectionTypeSecure[:]) == 1
		isInterm := subtle.ConstantTimeCompare(connType[:], mtgConnectionTypeIntermediate[:]) == 1
		isAbridg := subtle.ConstantTimeCompare(connType[:], mtgConnectionTypeAbridged[:]) == 1
		if !isSecure && !isInterm && !isAbridg {
			mtgctr.Release(recvCipher)
			continue
		}

		var rev [48]byte
		copy(rev[:], frame[8:56])
		slices.Reverse(rev[:])
		h.Reset()
		h.Write(rev[:32])
		h.Write(sec.Key[:])
		sendKey := h.Sum(nil)
		sendCipher, err := mtgctr.New(sendKey, rev[32:48])
		if err != nil {
			mtgctr.Release(recvCipher)
			continue
		}

		var dcBytes [2]byte
		copy(dcBytes[:], dec[60:62])
		dcRaw := int(int16(binary.LittleEndian.Uint16(dcBytes[:])))
		dc := dcRaw
		if dc < 0 {
			dc = -dc
		}
		if dc == 0 {
			dc = 2
		}

		return parsedObfuscated2{
			secretIdx:  idx,
			connType:   connType,
			dcBytes:    dcBytes,
			dc:         dc,
			recvCipher: recvCipher,
			sendCipher: sendCipher,
			matched:    true,
		}
	}
	return parsedObfuscated2{}
}

func matchObfuscated2Secret(frame []byte, secrets []mtglib.Secret) int {
	res := parseObfuscated2Frame(frame, secrets)
	mtgctr.Release(res.recvCipher)
	mtgctr.Release(res.sendCipher)
	if res.matched {
		return res.secretIdx
	}
	return 0
}

func (e *MtgEngine) handleRawConn(conn net.Conn, proxy *mtglib.Proxy, ntw mtglib.Network, secrets []mtglib.Secret) {
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var frame [64]byte
	if _, err := io.ReadFull(conn, frame[:]); err != nil {
		_ = conn.Close()
		return
	}
	_ = conn.SetReadDeadline(time.Time{})

	parsed := parseObfuscated2Frame(frame[:], secrets)
	defer mtgctr.Release(parsed.recvCipher)
	defer mtgctr.Release(parsed.sendCipher)
	if parsed.matched && parsed.connType != mtgConnectionTypeSecure {
		e.serveClassicObfuscated2(conn, ntw, parsed)
		return
	}

	mtgctr.Release(parsed.recvCipher)
	mtgctr.Release(parsed.sendCipher)

	wrapped := &prefixedEssentialsConn{
		Conn:   essentials.WrapNetConn(conn),
		prefix: frame[:],
	}
	_ = proxy.ServeWebConn(wrapped, parsed.secretIdx)
}

func (e *MtgEngine) serveClassicObfuscated2(clientRaw net.Conn, ntw mtglib.Network, parsed parsedObfuscated2) {
	defer clientRaw.Close()

	dcConn, dcRecvCipher, dcSendCipher, err := dialTelegramDCClassic(ntw, parsed.dc, parsed.connType, parsed.dcBytes)
	if err != nil {
		return
	}
	defer dcConn.Close()
	defer mtgctr.Release(dcRecvCipher)
	defer mtgctr.Release(dcSendCipher)

	e.connected.Add(1)
	defer func() {
		if v := e.connected.Add(-1); v < 0 {
			e.connected.Store(0)
		}
	}()

	clientStream := &obfuscatedStreamConn{
		Conn:       clientRaw,
		recvCipher: parsed.recvCipher,
		sendCipher: parsed.sendCipher,
	}
	dcStream := &obfuscatedStreamConn{
		Conn:       dcConn,
		recvCipher: dcRecvCipher,
		sendCipher: dcSendCipher,
	}

	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(dcStream, clientStream)
		if tc, ok := dcConn.(interface{ CloseWrite() error }); ok {
			_ = tc.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(clientStream, dcStream)
		if tc, ok := clientRaw.(interface{ CloseWrite() error }); ok {
			_ = tc.CloseWrite()
		}
		done <- struct{}{}
	}()
	<-done
	// Unblock the other copy and wait before releasing its cipher state.
	_ = clientRaw.Close()
	_ = dcConn.Close()
	<-done
}

func dialTelegramDCClassic(ntw mtglib.Network, dc int, connType [4]byte, dcBytes [2]byte) (net.Conn, cipher.Stream, cipher.Stream, error) {
	addrs := essentials.TelegramCoreAddresses[dc]
	if len(addrs) == 0 {
		addrs = essentials.TelegramCoreAddresses[2]
	}

	var v4Addrs, v6Addrs []string
	for _, a := range addrs {
		host, _, err := net.SplitHostPort(a)
		if err != nil {
			continue
		}
		if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
			v4Addrs = append(v4Addrs, a)
		} else {
			v6Addrs = append(v6Addrs, a)
		}
	}
	ordered := append(v4Addrs, v6Addrs...)

	var dcConn net.Conn
	var dialErr error
	for _, addr := range ordered {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		c, err := ntw.DialContext(ctx, "tcp", addr)
		cancel()
		if err == nil {
			dcConn = c
			break
		}
		dialErr = err
	}
	if dcConn == nil {
		if dialErr == nil {
			dialErr = errors.New("no telegram DC addresses available")
		}
		return nil, nil, nil, dialErr
	}

	var hs [64]byte
	for {
		if _, err := rand.Read(hs[:]); err != nil {
			_ = dcConn.Close()
			return nil, nil, nil, err
		}
		if hs[0] == 0xef {
			continue
		}
		switch binary.LittleEndian.Uint32(hs[:4]) {
		case 0x44414548, 0x54534f50, 0x20544547, 0x4954504f, 0x02010316, 0xdddddddd, 0xeeeeeeee:
			continue
		}
		if hs[4]|hs[5]|hs[6]|hs[7] == 0 {
			continue
		}
		copy(hs[56:60], connType[:])
		copy(hs[60:62], dcBytes[:])
		break
	}

	var origKeyIV [48]byte
	copy(origKeyIV[:], hs[8:56])

	sendCipher, err := mtgctr.New(origKeyIV[:32], origKeyIV[32:48])
	if err != nil {
		_ = dcConn.Close()
		return nil, nil, nil, err
	}

	var revKeyIV [48]byte
	copy(revKeyIV[:], origKeyIV[:])
	slices.Reverse(revKeyIV[:])
	recvCipher, err := mtgctr.New(revKeyIV[:32], revKeyIV[32:48])
	if err != nil {
		mtgctr.Release(sendCipher)
		_ = dcConn.Close()
		return nil, nil, nil, err
	}

	sendCipher.XORKeyStream(hs[:], hs[:])
	copy(hs[8:56], origKeyIV[:])

	_ = dcConn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := dcConn.Write(hs[:]); err != nil {
		mtgctr.Release(sendCipher)
		mtgctr.Release(recvCipher)
		_ = dcConn.Close()
		return nil, nil, nil, err
	}
	_ = dcConn.SetWriteDeadline(time.Time{})

	return dcConn, recvCipher, sendCipher, nil
}

type obfuscatedStreamConn struct {
	net.Conn
	recvCipher cipher.Stream
	sendCipher cipher.Stream
}

func (c *obfuscatedStreamConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.recvCipher.XORKeyStream(b[:n], b[:n])
	}
	return n, err
}

var obfuscatedWritePool = sync.Pool{
	New: func() any {
		buf := make([]byte, 32*1024)
		return &buf
	},
}

func (c *obfuscatedStreamConn) Write(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	pooled := obfuscatedWritePool.Get().(*[]byte)
	defer obfuscatedWritePool.Put(pooled)
	scratch := *pooled

	written := 0
	for written < len(b) {
		chunk := b[written:]
		if len(chunk) > len(scratch) {
			chunk = chunk[:len(scratch)]
		}
		dst := scratch[:len(chunk)]
		c.sendCipher.XORKeyStream(dst, chunk)
		n, err := c.Conn.Write(dst)
		written += n
		if err != nil {
			return written, err
		}
		if n < len(dst) {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}

type prefixedEssentialsConn struct {
	essentials.Conn
	prefix []byte
}

func (p *prefixedEssentialsConn) Read(b []byte) (int, error) {
	if len(p.prefix) > 0 {
		n := copy(b, p.prefix)
		p.prefix = p.prefix[n:]
		return n, nil
	}
	return p.Conn.Read(b)
}
