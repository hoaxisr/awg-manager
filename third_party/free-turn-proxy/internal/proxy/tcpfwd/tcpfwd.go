package tcpfwd

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/client/ish"
	"github.com/samosvalishe/free-turn-proxy/internal/clientsdb"
	"github.com/samosvalishe/free-turn-proxy/internal/logx"
	"github.com/samosvalishe/free-turn-proxy/internal/netconn"
	"github.com/samosvalishe/free-turn-proxy/internal/proxy/common"
	"github.com/samosvalishe/free-turn-proxy/internal/stats"
	"github.com/samosvalishe/free-turn-proxy/internal/transport/dtlsdial"
	"github.com/samosvalishe/free-turn-proxy/internal/transport/kcptun"
	"github.com/samosvalishe/free-turn-proxy/internal/wire"
	"github.com/xtaci/smux"
)

// GetCredsFunc реэкспортирован из common, чтобы вызывающие не выходили за пределы импортов пакета.
type GetCredsFunc = common.GetCredsFunc

// Params - конфигурация TURN/obf для пула.
type Params struct {
	Host         string
	Port         string
	TransportUDP bool
	Profile      string
	ObfKey       []byte
	GetCreds     GetCredsFunc
	KCPProfile   kcptun.Profile
	KCPFEC       kcptun.FEC
	ClientID     string
	TrafficStats *stats.Stats
}

// BondHandler распределяет одно принятое TCP-соединение по всем активным сессиям пула.
// Nil отключает bond-режим (будет round-robin).
// Реализация - internal/proxy/bondclient.
type BondHandler func(ctx context.Context, tcpConn net.Conn, connID uint64, lanes []*PooledSession)

// Deps - зависимости хост-процесса для TCP-forward цикла.
type Deps struct {
	DTLSDialer  *dtlsdial.Dialer
	Log         logx.Logger
	BondHandler BondHandler
}

func (d *Deps) log() logx.Logger {
	if d.Log == nil {
		return logx.Nop()
	}
	return d.Log
}

// Run - точка входа TCP-forward режима. Запускает numSessions maintainer-горутин, ждёт
// первого подключения, затем принимает локальные TCP-соединения и форвардит
// каждое как smux-поток (round-robin) или bonded по всем активным сессиям.
func Run(ctx context.Context, deps *Deps, params *Params, peer *net.UDPAddr, listenAddr string, numSessions int, useBond bool) error {
	pool := &SessionPool{}

	var wgMaint sync.WaitGroup
	for i := range numSessions {
		// streamID 1-based - единый базис с udprelay, чтобы vkauth.Store.CacheID
		// группировал потоки одинаково в обоих режимах. Стаггер от нуля: первая
		// сессия стартует без задержки.
		id := i + 1
		stagger := time.Duration(i) * 300 * time.Millisecond
		wgMaint.Go(func() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(stagger):
			}
			maintainSession(ctx, deps, params, peer, id, pool)
		})
	}

	deps.log().Infof("TCP mode: waiting for sessions to connect (total: %d)...", numSessions)
	select {
	case <-ctx.Done():
		wgMaint.Wait()
		return nil
	case <-pool.Ready():
	}

	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", listenAddr)
	if err != nil {
		wgMaint.Wait()
		return fmt.Errorf("tcpfwd listen %s: %w", listenAddr, err)
	}

	wrappedListener, err := ish.WrapListener(listener)
	if err != nil {
		deps.log().Warnf("failed to wrap listener: %v", err)
		wrappedListener = listener
	}

	context.AfterFunc(ctx, func() { _ = wrappedListener.Close() })
	if useBond {
		deps.log().Infof("TCP bond mode: listening on %s (striping each TCP connection across active sessions)", listenAddr)
	} else {
		deps.log().Infof("TCP mode: listening on %s (round-robin across %d sessions)", listenAddr, numSessions)
	}

	var wgConn sync.WaitGroup
	for {
		tcpConn, err := wrappedListener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				wgConn.Wait()
				wgMaint.Wait()
				return nil //nolint:nilerr // ctx cancel = clean shutdown, not an error
			}
			deps.log().Errorf("TCP accept error: %s", err)
			continue
		}

		if useBond {
			if deps.BondHandler == nil {
				deps.log().Errorf("bond requested but no BondHandler set, rejecting")
				_ = tcpConn.Close()
				continue
			}
			connID := (uint64(time.Now().UnixNano()) << 16) ^ pool.NextConnID()
			lanes := pool.Snapshot()
			if len(lanes) == 0 {
				deps.log().Errorf("No active sessions, rejecting connection")
				_ = tcpConn.Close()
				continue
			}

			tc, cid, lns := tcpConn, connID, lanes
			wgConn.Go(func() {
				deps.BondHandler(ctx, tc, cid, lns)
			})
			continue
		}

		ps := pool.Pick()
		if ps == nil || ps.Sess.IsClosed() {
			deps.log().Errorf("No active sessions, rejecting connection")
			_ = tcpConn.Close()
			continue
		}

		connID := pool.NextConnID()
		opened := ps.Opened.Add(1)
		active := ps.Active.Add(1)
		deps.log().Debugf("[session %d] TCP accept #%d from=%s active=%d opened=%d pool=%d",
			ps.ID, connID, tcpConn.RemoteAddr(), active, opened, pool.Count())

		tc, sessRef, cid := tcpConn, ps, connID
		wgConn.Go(func() {
			defer func() { _ = tc.Close() }()
			defer func() {
				active := sessRef.Active.Add(-1)
				closed := sessRef.Closed.Add(1)
				deps.log().Debugf("[session %d] TCP close #%d active=%d closed=%d totals: to-session=%s from-session=%s",
					sessRef.ID, cid, active, closed,
					stats.FormatByteCount(sessRef.ToSession.Load()), stats.FormatByteCount(sessRef.FromSession.Load()))
			}()

			stream, err := sessRef.Sess.OpenStream()
			if err != nil {
				deps.log().Errorf("[session %d] smux open stream error for TCP #%d: %s", sessRef.ID, cid, err)
				return
			}
			defer func() { _ = stream.Close() }()
			errf := func(format string, v ...any) {
				if deps.log().DebugEnabled() {
					deps.log().Errorf(format, v...)
				}
			}
			fromSession, toSession := netconn.BiCopy(ctx, tc, stream, errf)
			fromU, toU := nonNegUint64(fromSession), nonNegUint64(toSession)
			sessRef.FromSession.Add(fromU)
			sessRef.ToSession.Add(toU)
			deps.log().Debugf("[session %d] TCP done #%d local<-session=%s local->session=%s",
				sessRef.ID, cid, stats.FormatByteCount(fromU), stats.FormatByteCount(toU))
		})
	}
}

// maintainSession поддерживает одну TURN+DTLS+KCP+smux сессию живой:
// 3s backoff при ошибке инициализации, 2s после отключения успешной сессии,
// в обоих случаях перед следующей попыткой подключения.
func maintainSession(ctx context.Context, deps *Deps, params *Params, peer *net.UDPAddr, id int, pool *SessionPool) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		smuxSess, cleanup, err := createSmuxSession(ctx, deps, params, peer, id)
		if err != nil {
			deps.log().Errorf("[session %d] setup error: %s, retrying...", id, err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(3 * time.Second):
			}
			continue
		}

		ps := pool.Add(id, smuxSess)
		deps.log().Infof("[session %d] connected (active: %d)", id, pool.Count())

		for !smuxSess.IsClosed() {
			select {
			case <-ctx.Done():
				pool.Remove(ps)
				cleanup()
				return
			case <-time.After(1 * time.Second):
			}
		}

		pool.Remove(ps)
		cleanup()
		deps.log().Infof("[session %d] disconnected (active: %d), reconnecting...", id, pool.Count())

		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// createSmuxSession создаёт полный TURN+DTLS+KCP+smux pipeline и возвращает
// smux-сессию вместе с функцией cleanup (LIFO-разрушение).
func createSmuxSession(ctx context.Context, deps *Deps, params *Params, peer *net.UDPAddr, id int) (*smux.Session, func(), error) {
	var cleanupFns []func()
	cleanup := func() {
		for i := len(cleanupFns) - 1; i >= 0; i-- {
			cleanupFns[i]()
		}
	}

	stream, err := common.DialTURN(ctx, params.Host, params.Port, params.TransportUDP, peer, id, params.GetCreds)
	if err != nil {
		return nil, nil, err
	}
	cleanupFns = append(cleanupFns, func() { _ = stream.Close() })
	relayConn := stream.Relay
	deps.log().Debugf("[session %d] TURN server IP: %s", id, stream.ServerUDPAddr.IP)
	deps.log().Debugf("relayed-address=%s", relayConn.LocalAddr().String())

	obfConn, err := common.NewClientObf(params.Profile, params.ObfKey)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("obf init: %w", err)
	}
	dtlsPC := &wire.RelayPacketConn{Relay: relayConn, Peer: peer, Codec: obfConn}
	dtlsConn, err := deps.DTLSDialer.Dial(ctx, dtlsPC, peer)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("DTLS handshake: %w", err)
	}
	cleanupFns = append(cleanupFns, func() { _ = dtlsConn.Close() })
	deps.log().Debugf("DTLS connection established")

	// Client ID шлётся всегда первой DTLS app-record; сервер всегда читает.
	// -clients-file на сервере решает только, проверять ли ID по allowlist.
	if werr := clientsdb.WriteClientID(dtlsConn, params.ClientID); werr != nil {
		cleanup()
		return nil, nil, fmt.Errorf("send client ID: %w", werr)
	}

	var countedConn net.Conn = dtlsConn
	if params.TrafficStats != nil {
		countedConn = &stats.CountingConn{Conn: countedConn, Stats: params.TrafficStats}
	}
	kcpSess, err := kcptun.NewKCPOverDTLS(countedConn, false, params.KCPProfile, params.KCPFEC)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("KCP session: %w", err)
	}
	cleanupFns = append(cleanupFns, func() { _ = kcpSess.Close() })
	deps.log().Debugf("KCP session established")

	smuxSess, err := smux.Client(kcpSess, kcptun.DefaultSmuxConfig())
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("smux client: %w", err)
	}
	cleanupFns = append(cleanupFns, func() { _ = smuxSess.Close() })
	deps.log().Debugf("smux session established")

	return smuxSess, cleanup, nil
}

// nonNegUint64 безопасно приводит int64-счётчик байт к uint64 (io.Copy
// возвращает n >= 0 при успехе, но на ошибке n может быть отрицательным).
func nonNegUint64(n int64) uint64 {
	if n < 0 {
		return 0
	}
	return uint64(n)
}
