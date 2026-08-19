package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cbeuw/connutil"
	"github.com/pion/transport/v4/stdnet"
	"github.com/pion/turn/v5"
)

// pipeConn — stream поверх AsyncPacketPipe (plaintext через TURN/WRAP к peer).
type pipeConn struct {
	pc   net.PacketConn
	peer net.Addr
}

func (p *pipeConn) Read(b []byte) (int, error) {
	n, _, err := p.pc.ReadFrom(b)
	return n, err
}

func (p *pipeConn) Write(b []byte) (int, error) {
	return p.pc.WriteTo(b, p.peer)
}

func (p *pipeConn) Close() error                       { return p.pc.Close() }
func (p *pipeConn) LocalAddr() net.Addr                  { return p.pc.LocalAddr() }
func (p *pipeConn) RemoteAddr() net.Addr                 { return p.peer }
func (p *pipeConn) SetDeadline(t time.Time) error      { return p.pc.SetDeadline(t) }
func (p *pipeConn) SetReadDeadline(t time.Time) error  { return p.pc.SetReadDeadline(t) }
func (p *pipeConn) SetWriteDeadline(t time.Time) error { return p.pc.SetWriteDeadline(t) }

// RunRawSession — TURN + WRAP без DTLS; IP-пакеты и GETCONF_RAW через pipe к VPS -listen-raw.
func RunRawSession(
	ctx context.Context,
	tp *TurnParams,
	peer *net.UDPAddr,
	d *Dispatcher,
	getConfig bool,
	configCh chan<- string,
	sessionID int,
	creds *Credentials,
	deviceID, password string,
	stats *Stats,
) (bool, error) {
	configDelivered := false
	var firstWrapUp uint32
	var firstWrapDown uint32
	var firstPeerWrite uint32
	var firstPeerRead uint32

	if len(creds.TurnURLs) == 0 {
		return false, fmt.Errorf("нет TURN URL в учетных данных")
	}
	selectedURL := creds.TurnURLs[sessionID%len(creds.TurnURLs)]

	urlhost, urlport, err := net.SplitHostPort(selectedURL)
	if err != nil {
		return false, fmt.Errorf("разбор TURN URL %q: %w", selectedURL, err)
	}
	if tp.Host != "" {
		urlhost = tp.Host
	}
	if tp.Port != "" {
		urlport = tp.Port
	}
	turnAddr := net.JoinHostPort(urlhost, urlport)

	resolved, err := net.ResolveUDPAddr("udp", turnAddr)
	if err != nil {
		return false, fmt.Errorf("резолв TURN: %w", err)
	}
	c, err := net.DialUDP("udp", nil, resolved)
	if err != nil {
		return false, fmt.Errorf("подключение TURN UDP: %w", err)
	}
	defer c.Close()
	_ = c.SetReadBuffer(socketBufSize)
	_ = c.SetWriteBuffer(socketBufSize)
	var turnConn net.PacketConn = &connectedUDPConn{c}

	log.Printf("[RAW #%d] TURN UDP (%s)", sessionID, turnAddr)

	var addrFamily turn.RequestedAddressFamily
	if peer.IP.To4() != nil {
		addrFamily = turn.RequestedAddressFamilyIPv4
	} else {
		addrFamily = turn.RequestedAddressFamilyIPv6
	}

	tc, err := turn.NewClient(&turn.ClientConfig{
		STUNServerAddr:         turnAddr,
		TURNServerAddr:         turnAddr,
		Conn:                   turnConn,
		Net:                    new(stdnet.Net),
		Username:               creds.User,
		Password:               creds.Pass,
		RequestedAddressFamily: addrFamily,
		LoggerFactory:          &NullLoggerFactory{},
	})
	if err != nil {
		return false, fmt.Errorf("TURN клиент: %w", err)
	}
	defer tc.Close()

	if err = tc.Listen(); err != nil {
		return false, fmt.Errorf("TURN Listen: %w", err)
	}

	relay, err := tc.Allocate()
	if err != nil {
		if isAuthError(err) {
			handleAuthError(creds.CacheStreamID)
		}
		errStr := err.Error()
		if strings.Contains(errStr, "Quota") || strings.Contains(errStr, "486") {
			return false, fmt.Errorf("TURN квота: %w", err)
		}
		return false, fmt.Errorf("TURN Allocate: %w", err)
	}
	defer relay.Close()

	getStreamCache(creds.CacheStreamID).errorCount.Store(0)
	log.Printf("[RAW #%d] Relay: %s", sessionID, relay.LocalAddr())

	pipeA, pipeB := connutil.AsyncPacketPipe()
	plainConn := &pipeConn{pc: pipeB, peer: peer}
	// relay ↔ pipeA (как в RunSession); plaintext/GETCONF ↔ pipeB (как DTLS в RunSession).

	sessCtx, sessCancel := context.WithCancel(ctx)
	defer sessCancel()

	var sessionWg sync.WaitGroup
	sessionWg.Add(1)
	go func() {
		defer sessionWg.Done()
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-sessCtx.Done():
				return
			case <-t.C:
				tc.SendBindingRequest()
				_, _ = plainConn.Write([]byte{0xFF})
			}
		}
	}()

	var relayWg sync.WaitGroup
	relayWg.Add(2)

	useWrap := len(tp.WrapKey) == wrapKeyLen
	var obfsCfg *ObfsConfig
	var obfsWriteState *ObfsState
	if useWrap {
		obfsCfg = NewObfsConfig(tp.ObfsMode)
		obfsWriteState = NewObfsState()
	}

	stopRelay := context.AfterFunc(sessCtx, func() {
		_ = relay.SetDeadline(time.Now())
		_ = pipeA.SetDeadline(time.Now())
	})
	defer stopRelay()

	go func() {
		defer relayWg.Done()
		defer sessCancel()
		readBufLen := readBufSize + 80
		buf := make([]byte, readBufLen)
		plain := make([]byte, readBufSize)
		for {
			n, _, readErr := relay.ReadFrom(buf)
			if readErr != nil {
				return
			}
			payload := buf[:n]
			if useWrap {
				if !obfsIsRTPPacket(payload) {
					log.Printf("[RAW #%d] OBFS unwrap: unexpected packet (n=%d)", sessionID, n)
					continue
				}
				m, wrapErr := obfsUnwrapPacket(tp.WrapKey, payload, plain)
				if wrapErr != nil {
					log.Printf("[RAW #%d] OBFS unwrap: %v (n=%d)", sessionID, wrapErr, n)
					continue
				}
				payload = plain[:m]
			}
			if atomic.CompareAndSwapUint32(&firstWrapUp, 0, 1) {
				log.Printf("[RAW #%d] [ДЕБАГ] Первый пакет от TURN Relay (%d байт)", sessionID, len(payload))
			}
			if _, writeErr := pipeA.WriteTo(payload, peer); writeErr != nil {
				return
			}
		}
	}()

	go func() {
		defer relayWg.Done()
		defer sessCancel()
		b := make([]byte, readBufSize)
		for {
			n, _, readErr := pipeA.ReadFrom(b)
			if readErr != nil {
				return
			}
			out := b[:n]
			if useWrap {
				if obfsCfg != nil && obfsWriteState != nil {
					wrapped, wrapErr := obfsWrapPacket(tp.WrapKey, out, obfsCfg, obfsWriteState)
					if wrapErr != nil {
						log.Printf("[RAW #%d] OBFS wrap: %v", sessionID, wrapErr)
						return
					}
					out = wrapped
				}
			}
			if atomic.CompareAndSwapUint32(&firstWrapDown, 0, 1) {
				log.Printf("[RAW #%d] [ДЕБАГ] Первый пакет на TURN Relay (%d байт)", sessionID, len(out))
			}
			if _, writeErr := relay.WriteTo(out, peer); writeErr != nil {
				return
			}
		}
	}()

	stats.ActiveConnections.Add(1)
	defer stats.ActiveConnections.Add(-1)

	// APK: GETCONF_RAW только worker #1; relay 2..N регистрируются uplink IP.
	var conf RawConf
	var confErr error
	if getConfig {
		conf, confErr = RequestRawConfig(plainConn, deviceID, password)
		if confErr != nil {
			errStr := confErr.Error()
			if strings.Contains(errStr, "FATAL_AUTH") {
				return false, confErr
			}
			if configCh != nil {
				log.Printf("[RAW #%d] Ошибка RAWCONF: %v", sessionID, confErr)
			}
		} else if configCh != nil {
			payload := fmt.Sprintf("%s|%s|%d", conf.ClientIP, conf.DNS, conf.MTU)
			select {
			case configCh <- payload:
				configDelivered = true
				log.Printf("[RAW #%d] RAWCONF получен: %s", sessionID, payload)
			default:
				configDelivered = true
				log.Printf("[RAW #%d] RAWCONF уже был доставлен другим воркером", sessionID)
			}
		}
	} else {
		if err := SendRawAuth(plainConn, deviceID, password); err != nil {
			log.Printf("[RAW #%d] Ошибка AUTH: %v", sessionID, err)
			return false, err
		}
		log.Printf("[RAW #%d] Relay готов (AUTH отправлен)", sessionID)
	}

	log.Printf("[RAW #%d] [READY] Raw-туннель готов ✓", sessionID)

	slot := &WorkerSlot{
		ID:     sessionID,
		SendCh: make(chan []byte, workerSendBuf),
	}
	d.Register(slot)
	defer d.Unregister(slot)

	var proxyWg sync.WaitGroup
	proxyWg.Add(2)

	stopPlain := context.AfterFunc(sessCtx, func() {
		_ = plainConn.SetDeadline(time.Now())
	})
	defer stopPlain()

	go func() {
		defer proxyWg.Done()
		defer sessCancel()
		for {
			select {
			case <-sessCtx.Done():
				return
			case pkt, ok := <-slot.SendCh:
				if !ok {
					return
				}
				_ = plainConn.SetWriteDeadline(time.Now().Add(sessionReadTimeout))
				if atomic.CompareAndSwapUint32(&firstPeerWrite, 0, 1) {
					log.Printf("[RAW #%d] [ДЕБАГ] Первый IP-пакет на peer (%d байт)", sessionID, len(pkt))
				}
				_, writeErr := plainConn.Write(pkt)
				putPktBuf(pkt)
				if writeErr != nil {
					log.Printf("[RAW #%d] Ошибка Writer: %v", sessionID, writeErr)
					return
				}
			}
		}
	}()

	go func() {
		defer proxyWg.Done()
		defer sessCancel()
		b := make([]byte, 2000)
		for {
			_ = plainConn.SetReadDeadline(time.Now().Add(sessionReadTimeout))
			n, readErr := plainConn.Read(b)
			if readErr != nil {
				if sessCtx.Err() != nil {
					return
				}
				if ne, ok := readErr.(net.Error); ok && ne.Timeout() {
					continue
				}
				log.Printf("[RAW #%d] Ошибка Reader: %v", sessionID, readErr)
				return
			}
			if n <= 0 {
				continue
			}
			if b[0] == 0xFF {
				continue
			}
			if atomic.CompareAndSwapUint32(&firstPeerRead, 0, 1) {
				log.Printf("[RAW #%d] [ДЕБАГ] Первый IP-пакет от peer (%d байт)", sessionID, n)
			}
			pkt := getPktBuf(n)
			copy(pkt, b[:n])
			select {
			case d.ReturnCh <- pkt:
			case <-sessCtx.Done():
				putPktBuf(pkt)
				return
			}
		}
	}()

	proxyWg.Wait()
	sessCancel()
	relayWg.Wait()
	sessionWg.Wait()
	_ = pipeA.Close()
	_ = pipeB.Close()
	log.Printf("[RAW #%d] Завершена", sessionID)
	return configDelivered, nil
}
