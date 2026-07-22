// Package dnsdial владеет DNS-резолвингом и net.Dialer'ом, прокинутым во все
// outbound HTTP/TLS клиенты. По Mode выбирает UDP/53, DNS-over-HTTPS или auto
// (UDP-probe → sticky DoH fallback).
package dnsdial

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/miekg/dns"

	"github.com/samosvalishe/free-turn-proxy/internal/logx"
	"github.com/samosvalishe/free-turn-proxy/internal/netctl"

	// встроенные Mozilla CA roots для CGO_ENABLED=0 сборок (Android).
	_ "golang.org/x/crypto/x509roots/fallback"
)

// Log - пакетный логгер. По умолчанию no-op; main устанавливает через SetLogger.
var Log logx.Logger = logx.Nop()

// SetLogger ставит логгер пакета.
func SetLogger(l logx.Logger) { Log = logx.OrNop(l) }

const (
	dohQueryTimeout = 6 * time.Second
	// общий бюджет для всех попыток endpoint'ов в forwardRaw. Кратно
	// dohQueryTimeout - чтобы каждому fallback хватило времени.
	dohForwardBudget    = 25 * time.Second
	dohMaxResponseBytes = 64 * 1024
	dohContentType      = "application/dns-message"

	dohDialerTimeout   = 5 * time.Second
	dohDialerKeepAlive = 30 * time.Second
	appDialerTimeout   = 20 * time.Second
	appDialerKeepAlive = 30 * time.Second

	forwarderUDPBufSize = 4096
	forwarderTCPReadDL  = 30 * time.Second
	forwarderTCPWriteDL = 10 * time.Second
	autoUDPBudget       = 1500 * time.Millisecond
)

// DohEndpoint - один DNS-over-HTTPS сервер с bootstrap-IP, чтобы резолв самого
// hostname не требовал DNS.
type DohEndpoint struct {
	URL          string
	Hostname     string
	BootstrapIPs []string
}

// Yandex - первый, т.к. остаётся доступен у RU мобильных операторов даже когда
// международные резолверы блокируются; Google и Cloudflare - fallback.
var defaultDohEndpoints = []DohEndpoint{
	{"https://common.dot.dns.yandex.net/dns-query", "common.dot.dns.yandex.net", []string{"77.88.8.8", "77.88.8.1"}},
	{"https://secure.dot.dns.yandex.net/dns-query", "secure.dot.dns.yandex.net", []string{"77.88.8.88", "77.88.8.2"}},
	{"https://family.dot.dns.yandex.net/dns-query", "family.dot.dns.yandex.net", []string{"77.88.8.7", "77.88.8.3"}},
}

// DohResolver делает POST с DNS-wire запросом к одному из DoH endpoint'ов.
type DohResolver struct {
	endpoints []DohEndpoint
	client    *http.Client
}

// NewDohResolver конструирует резолвер; если endpoints=nil, берёт
// defaultDohEndpoints. Имена endpoint'ов диалятся по BootstrapIPs - DoH-транспорт
// не зависит от системного резолвера.
func NewDohResolver(endpoints []DohEndpoint) *DohResolver {
	if len(endpoints) == 0 {
		endpoints = defaultDohEndpoints
	}
	return &DohResolver{
		endpoints: endpoints,
		client:    &http.Client{Timeout: dohQueryTimeout, Transport: newBootstrapTransport(endpoints)},
	}
}

// newDohResolverWithClient - тестовый hook, минующий bootstrap-транспорт.
func newDohResolverWithClient(endpoints []DohEndpoint, client *http.Client) *DohResolver {
	return &DohResolver{endpoints: endpoints, client: client}
}

// newBootstrapTransport возвращает http.Transport, чей DialContext знает только
// заданные hostname'ы DoH endpoint'ов и резолвит их в BootstrapIPs.
func newBootstrapTransport(endpoints []DohEndpoint) *http.Transport {
	bootstrap := make(map[string][]string, len(endpoints))
	for _, ep := range endpoints {
		bootstrap[ep.Hostname] = ep.BootstrapIPs
	}
	dialer := &net.Dialer{Timeout: dohDialerTimeout, KeepAlive: dohDialerKeepAlive, Control: netctl.Apply}

	return &http.Transport{
		MaxIdleConns:        8,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   true,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, ok := bootstrap[host]
			if !ok {
				return nil, fmt.Errorf("doh: no bootstrap IPs for %q", host)
			}
			var lastErr error
			for _, ip := range ips {
				conn, derr := dialer.DialContext(ctx, network, net.JoinHostPort(ip, port))
				if derr == nil {
					return conn, nil
				}
				lastErr = derr
			}
			return nil, lastErr
		},
		// явный DialTLSContext гарантирует SNI = hostname, даже если TCP-dial
		// идёт на bootstrap-IP. Без этого некоторые HTTP/2 пути сливают
		// литерал IP как ServerName и TLS падает.
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, ok := bootstrap[host]
			if !ok {
				return nil, fmt.Errorf("doh: no bootstrap IPs for %q", host)
			}
			var lastErr error
			for _, ip := range ips {
				rawConn, derr := dialer.DialContext(ctx, network, net.JoinHostPort(ip, port))
				if derr != nil {
					lastErr = derr
					continue
				}
				tlsConn := tls.Client(rawConn, &tls.Config{
					MinVersion: tls.VersionTLS12,
					ServerName: host,
					NextProtos: []string{"h2", "http/1.1"},
				})
				if err := tlsConn.HandshakeContext(ctx); err != nil {
					_ = rawConn.Close()
					lastErr = err
					continue
				}
				return tlsConn, nil
			}
			return nil, lastErr
		},
	}
}

// forwardRaw делает POST opaque DNS-wire запроса к настроенным DoH endpoint'ам
// по порядку и возвращает первый успешный raw-ответ вместе с endpoint'ом.
// Без парсинга - удобно для локального форвардера, который пропускает что бы
// upstream ни ответил (RESINFO/HTTPS/SVCB/EDNS options/…).
//
// Каждому endpoint'у - свой per-attempt deadline (dohQueryTimeout), чтобы
// медленный первый не сожрал весь бюджет и не зарезал fallback'и. Parent ctx
// всё ещё ограничивает общее ожидание через cancel chain.
func (r *DohResolver) forwardRaw(ctx context.Context, query []byte) ([]byte, error) {
	if len(r.endpoints) == 0 {
		return nil, errors.New("doh: no endpoints configured")
	}
	var lastErr error
	for _, ep := range r.endpoints {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		epCtx, cancel := context.WithTimeout(ctx, dohQueryTimeout)
		body, err := r.postWire(epCtx, ep, query)
		cancel()
		if err != nil {
			Log.Warnf("[DoH] %s: %v", ep.Hostname, err)
			lastErr = err
			continue
		}
		return body, nil
	}
	return nil, lastErr
}

// postWire делает один application/dns-message POST к одному endpoint'у.
func (r *DohResolver) postWire(ctx context.Context, ep DohEndpoint, query []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", ep.URL, bytes.NewReader(query))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", dohContentType)
	req.Header.Set("Accept", dohContentType)

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body) //nolint:errcheck
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, dohMaxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return body, nil
}

// net.Resolver Go дозванивается до этого stub'а как до обычного nameserver'а -
// обходит уйму edge-case'ов fake-net.Conn (RESINFO probes, EDNS handshakes,
// truncation, …). Что бы он ни прочитал на UDP/TCP - уходит дословно в DoH
// endpoint, ответ отдаётся клиенту.

type dohForwarder struct {
	udpAddr string
	tcpAddr string
}

var (
	dohForwarderOnce sync.Once
	dohForwarderInst *dohForwarder
	dohForwarderErr  error
)

// sharedDohForwarder лениво запускает process-wide форвардер, привязанный к
// заданному resolver. Побеждает первый caller; следующие переиспользуют тот же
// форвардер независимо от того, что передали.
func sharedDohForwarder(r *DohResolver) (*dohForwarder, error) {
	dohForwarderOnce.Do(func() {
		dohForwarderInst, dohForwarderErr = startDohForwarder(r)
	})
	return dohForwarderInst, dohForwarderErr
}

func startDohForwarder(r *DohResolver) (_ *dohForwarder, err error) {
	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		return nil, fmt.Errorf("doh forwarder: listen UDP: %w", err)
	}
	defer func() {
		if err != nil {
			_ = udpConn.Close()
		}
	}()
	tcpLn, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		return nil, fmt.Errorf("doh forwarder: listen TCP: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tcpLn.Close()
		}
	}()

	fwd := &dohForwarder{
		udpAddr: udpConn.LocalAddr().String(),
		tcpAddr: tcpLn.Addr().String(),
	}
	Log.Infof("[DoH] forwarder listening udp=%s tcp=%s", fwd.udpAddr, fwd.tcpAddr)

	go fwd.serveUDP(udpConn, r)
	go fwd.serveTCP(tcpLn, r)
	return fwd, nil
}

func (*dohForwarder) serveUDP(conn *net.UDPConn, r *DohResolver) {
	defer func() { _ = conn.Close() }()
	buf := make([]byte, forwarderUDPBufSize)
	for {
		n, client, err := conn.ReadFromUDP(buf)
		if err != nil {
			Log.Errorf("[DoH] udp read: %v", err)
			return
		}
		query := append([]byte(nil), buf[:n]...)
		go func(q []byte, c *net.UDPAddr) {
			ctx, cancel := context.WithTimeout(context.Background(), dohForwardBudget)
			defer cancel()
			resp, err := r.forwardRaw(ctx, q)
			if err != nil {
				Log.Warnf("[DoH] udp forward failed: %v", err)
				return
			}
			if _, err := conn.WriteToUDP(resp, c); err != nil {
				Log.Warnf("[DoH] udp write: %v", err)
			}
		}(query, client)
	}
}

func (*dohForwarder) serveTCP(ln *net.TCPListener, r *DohResolver) {
	defer func() { _ = ln.Close() }()
	for {
		conn, err := ln.Accept()
		if err != nil {
			Log.Errorf("[DoH] tcp accept: %v", err)
			return
		}
		go handleDohForwarderTCP(conn, r)
	}
}

func handleDohForwarderTCP(conn net.Conn, r *DohResolver) {
	defer func() { _ = conn.Close() }()
	for {
		_ = conn.SetReadDeadline(time.Now().Add(forwarderTCPReadDL)) //nolint:errcheck
		var lenBuf [2]byte
		if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
			return
		}
		qlen := int(lenBuf[0])<<8 | int(lenBuf[1])
		if qlen == 0 || qlen > forwarderUDPBufSize {
			return
		}
		query := make([]byte, qlen)
		if _, err := io.ReadFull(conn, query); err != nil {
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), dohQueryTimeout)
		resp, err := r.forwardRaw(ctx, query)
		cancel()
		if err != nil {
			Log.Warnf("[DoH] tcp forward failed: %v", err)
			return
		}
		if len(resp) > 0xFFFF {
			Log.Warnf("[DoH] response too large for TCP framing: %d", len(resp))
			return
		}
		out := make([]byte, 2+len(resp))
		respLen := uint16(len(resp)) //nolint:gosec // bounded above
		binary.BigEndian.PutUint16(out[:2], respLen)
		copy(out[2:], resp)
		_ = conn.SetWriteDeadline(time.Now().Add(forwarderTCPWriteDL)) //nolint:errcheck
		if _, err := conn.Write(out); err != nil {
			return
		}
	}
}

// dohForwarderDial возвращает Resolver.Dial, подключающийся к локальному DoH
// форвардеру по UDP или TCP (что запросил резолвер).
func dohForwarderDial(r *DohResolver) dialFunc {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		fwd, err := sharedDohForwarder(r) //nolint:contextcheck
		if err != nil {
			return nil, err
		}
		var d net.Dialer
		switch network {
		case "tcp", "tcp4", "tcp6":
			return d.DialContext(ctx, "tcp", fwd.tcpAddr)
		default:
			return d.DialContext(ctx, "udp", fwd.udpAddr)
		}
	}
}

const (
	DNSModePlain = "plain"
	DNSModeDoH   = "doh"
	DNSModeAuto  = "auto"
)

var udpDNSServersPtr atomic.Pointer[[]string]

func init() {
	def := []string{
		"77.88.8.8:53", "77.88.8.1:53",
		"8.8.8.8:53", "8.8.4.4:53",
		"1.1.1.1:53", "1.0.0.1:53",
	}
	udpDNSServersPtr.Store(&def)
}

func udpDNSServers() []string { return *udpDNSServersPtr.Load() }

// SetUDPDNSServers заменяет дефолтный список UDP/53 серверов. Каждый элемент -
// "ip" или "ip:port"; голые IP получают :53. Пустой список оставляет дефолт.
// На Android используется для подсовывания резолверов оператора из
// LinkProperties.dnsServers - часто работают там, где публичный DoH/DoT нет.
//
// Безопасно вызывать конкурентно с использованием резолвера - указатель списка
// меняется атомарно. Уже идущие dial видят свой захваченный снимок.
func SetUDPDNSServers(servers []string) {
	if len(servers) == 0 {
		return
	}
	normalized := make([]string, 0, len(servers))
	for _, s := range servers {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(s); err != nil {
			s = net.JoinHostPort(s, "53")
		}
		normalized = append(normalized, s)
	}
	if len(normalized) == 0 {
		return
	}
	udpDNSServersPtr.Store(&normalized)
}

type dialFunc = func(context.Context, string, string) (net.Conn, error)

// buildDialer возвращает net.Dialer, чей внутренний Go-резолвер использует
// выбранный DNS-транспорт. В режиме "auto" первый полный отказ UDP/53
// залипает процесс на DoH до конца его жизни.
func buildDialer(mode string, r *DohResolver) net.Dialer {
	switch mode {
	case DNSModePlain:
		return newAppDialer(udpDNSDial)
	case DNSModeDoH:
		return newAppDialer(dohForwarderDial(r))
	case DNSModeAuto:
		return newAppDialer(autoDial(r))
	default:
		panic(fmt.Sprintf("unknown DNS mode %q", mode))
	}
}

// newAppDialer оборачивает Resolver.Dial таймаутами, используемыми везде в
// приложении для outbound TCP/HTTP.
func newAppDialer(dial dialFunc) net.Dialer {
	return net.Dialer{
		Timeout:   appDialerTimeout,
		KeepAlive: appDialerKeepAlive,
		Resolver:  &net.Resolver{PreferGo: true, Dial: dial},
		Control:   netctl.Apply,
	}
}

// udpDNSDial берёт первый достижимый UDP/53 резолвер из udpDNSServers.
func udpDNSDial(ctx context.Context, _ string, _ string) (net.Conn, error) {
	var (
		d       net.Dialer
		lastErr error
	)
	for _, s := range udpDNSServers() {
		conn, err := d.DialContext(ctx, "udp", s)
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("no UDP DNS servers available")
	}
	return nil, lastErr
}

// autoDial возвращает Dial, который один раз пробит UDP/53 реальным DNS
// round-trip'ом; при провале залипает на DoH до конца процесса. Сделано под
// Android, где сеть скачет между Wi-Fi (UDP/53 работает) и мобилкой (блок).
//
// Просто dial-timeout не годится: UDP "dial" безсоединительный и всегда успешен
// мгновенно. Единственный способ узнать - реально отправить запрос и ждать ответ.
func autoDial(r *DohResolver) dialFunc {
	var (
		probed sync.Once
		useDoH atomic.Bool
		doh    = dohForwarderDial(r)
	)
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		probed.Do(func() {
			if udpProbe(autoUDPBudget) {
				Log.Infof("[DNS] UDP/53 probe OK, using UDP")
			} else {
				Log.Warnf("[DNS] UDP/53 unreachable; sticky-switching to DoH")
				useDoH.Store(true)
			}
		})
		if useDoH.Load() {
			return doh(ctx, network, addr)
		}
		return udpDNSDial(ctx, network, addr)
	}
}

// udpProbe шлёт реальный DNS A-запрос к known-домену по UDP и проверяет, придёт
// ли ответ до дедлайна. Пробуем первые два сервера из udpDNSServers под общим
// deadline - если ни один не ответил, UDP/53 заблокирован.
func udpProbe(timeout time.Duration) bool {
	m := new(dns.Msg)
	m.SetQuestion("dns.google.", dns.TypeA)
	m.RecursionDesired = true
	wire, err := m.Pack()
	if err != nil {
		return false
	}

	deadline := time.Now().Add(timeout)
	buf := make([]byte, 512)
	servers := udpDNSServers()
	limit := min(len(servers), 2)
	for _, server := range servers[:limit] {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		conn, err := net.DialTimeout("udp", server, remaining) //nolint:noctx
		if err != nil {
			continue
		}
		_ = conn.SetDeadline(deadline) //nolint:errcheck
		_, _ = conn.Write(wire)
		n, err := conn.Read(buf)
		_ = conn.Close()
		if err == nil && n > 12 {
			return true
		}
	}
	return false
}
