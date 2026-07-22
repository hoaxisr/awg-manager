package udprelay

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cbeuw/connutil"
	"github.com/samosvalishe/free-turn-proxy/internal/clientsdb"
	"github.com/samosvalishe/free-turn-proxy/internal/provider"
	"github.com/samosvalishe/free-turn-proxy/internal/proxy/common"
	"github.com/samosvalishe/free-turn-proxy/internal/randx"
	"github.com/samosvalishe/free-turn-proxy/internal/wire/shape"
)

// DTLSLoop поддерживает единственное DTLS-подключение для streamID, перезапуская
// его при сбое с backoff 10-30s (пропускается при активном provider-backoff,
// если предыдущая ошибка - дедлайн). connchan получает свежую половину
// AsyncPacketPipe на каждой попытке; okchan (non-nil только для потока 1)
// сигнализирует о первом успешном handshake.
func DTLSLoop(ctx context.Context, deps *Deps, params *Params, peer *net.UDPAddr, listenConn net.PacketConn, inboundChan <-chan *Packet, connchan chan<- net.PacketConn, okchan chan<- struct{}, streamID int) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			err := oneDTLS(ctx, deps, params, peer, listenConn, inboundChan, connchan, okchan, streamID)
			// При активном provider-backoff дедлайн handshake срабатывает раньше,
			// чем auth-retry успевает отработать; делаем краткий backoff,
			// чтобы не крутиться в tight spin до снятия блокировки.
			if err != nil && time.Now().Unix() < deps.Auth.BackoffUntilUnix() && errors.Is(err, context.DeadlineExceeded) {
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Duration(1+randx.Intn(2)) * time.Second):
				}
				continue
			}
			if err != nil {
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Duration(10+randx.Intn(20)) * time.Second):
				}
			}
		}
	}
}

// TURNLoop ведёт половину TURN-аллокации. Ждёт свежий conn2 от DTLS-цикла,
// тормозит через t (глобальный тик 200ms), выполняет одну TURN-сессию
// и реагирует на provider.ErrFatalNoStreams / provider.ErrBackoffActive
// соответственно.
func TURNLoop(ctx context.Context, deps *Deps, params *Params, peer *net.UDPAddr, connchan <-chan net.PacketConn, t <-chan time.Time, streamID int) {
	for {
		select {
		case <-ctx.Done():
			return
		case conn2 := <-connchan:
			select {
			case <-t:
			case <-ctx.Done():
				return
			}
			c := make(chan error, 1)
			go oneTURN(ctx, deps, params, peer, conn2, streamID, c)

			var err error
			select {
			case err = <-c:
			case <-ctx.Done():
				return
			}
			if err != nil {
				if errors.Is(err, provider.ErrFatalNoStreams) {
					deps.log().Errorf("[STREAM %d] Fatal provider error. Shutting down application.", streamID)
					select {
					case deps.fatalCh <- fmt.Errorf("%w: %w", ErrFatal, err):
					default:
					}
					return
				}
				if errors.Is(err, provider.ErrBackoffActive) {
					lockoutEnd := deps.Auth.BackoffUntilUnix()
					var sleepDuration time.Duration
					if lockoutEnd > 0 {
						sleepDuration = time.Until(time.Unix(lockoutEnd, 0))
						if sleepDuration < 0 {
							sleepDuration = 5 * time.Second
						}
					} else {
						sleepDuration = 60 * time.Second
						deps.log().Warnf("[STREAM %d] Backing off for 60 seconds (provider requests wait)", streamID)
					}
					select {
					case <-ctx.Done():
						return
					case <-time.After(sleepDuration):
					}
				} else {
					deps.log().Errorf("[STREAM %d] %s", streamID, err)
					select {
					case <-ctx.Done():
						return
					case <-time.After(2 * time.Second):
					}
				}
			}
		}
	}
}

func oneDTLS(ctx context.Context, deps *Deps, params *Params, peer *net.UDPAddr, listenConn net.PacketConn, inboundChan <-chan *Packet, connchan chan<- net.PacketConn, okchan chan<- struct{}, streamID int) error {
	select {
	case <-time.After(time.Duration(randx.Intn(400)+100) * time.Millisecond):
	case <-ctx.Done():
		return ctx.Err()
	}

	dtlsctx, dtlscancel := context.WithCancel(ctx)
	defer dtlscancel()

	conn1, conn2 := connutil.AsyncPacketPipe()
	defer func() { _ = conn1.Close() }()
	defer func() { _ = conn2.Close() }()
	// TURNLoop может перезапускать oneTURN несколько раз в рамках одного DTLS
	// соединения, каждый раз перечитывая conn2; публикуем до завершения DTLS.
	go func() {
		for {
			select {
			case <-dtlsctx.Done():
				return
			case connchan <- conn2:
			}
		}
	}()
	dtlsRaw, err1 := deps.DTLSDialer.Dial(dtlsctx, conn1, peer)
	if err1 != nil {
		return fmt.Errorf("failed to connect DTLS: %w", err1)
	}
	var dtlsConn net.Conn = dtlsRaw
	defer func() {
		if closeErr := dtlsConn.Close(); closeErr != nil {
			deps.log().Errorf("[STREAM %d] failed to close DTLS connection: %s", streamID, closeErr)
		}
		deps.log().Infof("[STREAM %d] Closed DTLS connection", streamID)
	}()
	deps.log().Infof("[STREAM %d] Established DTLS connection", streamID)

	// Client ID шлётся всегда (первой DTLS app-record); сервер всегда читает.
	// -clients-file на сервере решает только, проверять ли ID по allowlist.
	if err := clientsdb.WriteClientID(dtlsConn, params.ClientID); err != nil {
		return fmt.Errorf("failed to write client ID: %w", err)
	}

	if okchan != nil {
		go func() {
			select {
			case okchan <- struct{}{}:
			case <-dtlsctx.Done():
			}
		}()
	}

	wg := sync.WaitGroup{}
	context.AfterFunc(dtlsctx, func() {
		if err := dtlsConn.SetDeadline(time.Now()); err != nil {
			deps.log().Warnf("[STREAM %d] SetDeadline failed: %v", streamID, err)
		}
	})

	wg.Go(func() {
		defer dtlscancel()
		for {
			select {
			case <-dtlsctx.Done():
				return
			case pkt := <-inboundChan:
				_, werr := dtlsConn.Write(pkt.Data[:pkt.N])
				packetPool.Put(pkt)
				if werr != nil {
					deps.log().Debugf("[STREAM %d] DTLS write error: %v", streamID, werr)
					return
				}
			}
		}
	})

	wg.Go(func() {
		defer dtlscancel()
		buf := make([]byte, 1600)
		for {
			n, err1 := dtlsConn.Read(buf)
			if err1 != nil {
				return
			}

			if peerAddr := deps.ActiveLocalPeer.Load(); peerAddr != nil {
				if addr, ok := peerAddr.(net.Addr); ok {
					if _, err := listenConn.WriteTo(buf[:n], addr); err != nil {
						deps.log().Errorf("[STREAM %d] failed to forward packet to local peer: %v", streamID, err)
					}
				}
			}
		}
	})

	wg.Wait()
	if err := dtlsConn.SetDeadline(time.Time{}); err != nil {
		deps.log().Errorf("[STREAM %d] Failed to clear DTLS deadline: %s", streamID, err)
	}
	return nil
}

func oneTURN(ctx context.Context, deps *Deps, params *Params, peer *net.UDPAddr, conn2 net.PacketConn, streamID int, c chan<- error) {
	var err error
	defer func() { c <- err }()
	select {
	case <-time.After(time.Duration(randx.Intn(400)+100) * time.Millisecond):
	case <-ctx.Done():
		err = ctx.Err()
		return
	}
	stream, err1 := common.DialTURN(ctx, params.Host, params.Port, params.TransportUDP, peer, streamID, params.GetCreds)
	if err1 != nil {
		if deps.Auth.IsAuthError(err1) {
			deps.Auth.HandleAuthError(streamID)
		}
		err = err1
		return
	}
	relayConn := stream.Relay
	deps.log().Debugf("[STREAM %d] TURN server IP: %s", streamID, stream.ServerUDPAddr.IP)

	if params.ObfTiming > 0 {
		relayConn = shape.WrapPacketConn(relayConn, params.ObfTiming)
		deps.log().Debugf("[STREAM %d] obf-timing=%s", streamID, params.ObfTiming)
	}

	// Инкремент до ResetErrors - конкурентные наблюдатели HandleAuthError видят
	// поток подключённым до сброса счётчика ошибок.
	deps.ConnectedStreams.Add(1)
	deps.Auth.ResetErrors(streamID)

	defer func() {
		deps.ConnectedStreams.Add(-1)
		if cerr := stream.Close(); cerr != nil {
			err = fmt.Errorf("failed to close TURN stream: %s", cerr)
		}
	}()

	deps.log().Debugf("[STREAM %d] relayed-address=%s", streamID, relayConn.LocalAddr().String())

	wg := sync.WaitGroup{}
	turnctx, turncancel := context.WithCancel(ctx)

	context.AfterFunc(turnctx, func() {
		if err := relayConn.SetDeadline(time.Now()); err != nil {
			deps.log().Errorf("Failed to set relay deadline: %s", err)
		}
	})
	var internalPipeAddr atomic.Value
	obfConn, obfErr := common.NewClientObf(params.Profile, params.ObfKey)
	if obfErr != nil {
		deps.log().Errorf("[STREAM %d] OBF init failed: %v", streamID, obfErr)
		turncancel()
		return
	}

	const maxPayload = 1600

	// PermDead закрывается при блэкхоле data-path (см. turndial/permwatch.go) -
	// отменяем turnctx, TURNLoop делает свежий allocate.
	wg.Go(func() {
		select {
		case <-turnctx.Done():
		case <-stream.PermDead:
			deps.log().Warnf("[STREAM %d] TURN channel-bind умер - рецикл allocation", streamID)
			turncancel()
		}
	})

	wg.Go(func() {
		defer turncancel()
		// При obf читаем payload сразу в buf[HeaderLen:], чтобы WrapInPlace
		// дописал заголовок+tag без копии payload.
		var buf, readSlot []byte
		if obfConn != nil {
			buf = make([]byte, obfConn.MaxWire(maxPayload))
			readSlot = buf[obfConn.HeaderLen() : obfConn.HeaderLen()+maxPayload]
		} else {
			buf = make([]byte, maxPayload)
			readSlot = buf
		}
		// Адрес внутреннего пайпа константен; фиксируем один раз вместо
		// atomic-записи на каждый пакет.
		addrStored := false
		for {
			if turnctx.Err() != nil {
				return
			}
			n, addr1, err1 := conn2.ReadFrom(readSlot)
			if err1 != nil {
				return
			}
			if turnctx.Err() != nil {
				return
			}

			if !addrStored {
				internalPipeAddr.Store(addr1)
				addrStored = true
			}

			out := readSlot[:n]
			if obfConn != nil {
				written, wErr := obfConn.WrapInPlace(buf, n)
				if wErr != nil {
					deps.log().Errorf("[STREAM %d] OBF wrap failed: %v", streamID, wErr)
					return
				}
				out = buf[:written]
			}

			written, err1 := relayConn.WriteTo(out, peer)
			if params.TrafficStats != nil {
				params.TrafficStats.AddTx(written)
			}
			if err1 != nil {
				return
			}
		}
	})

	wg.Go(func() {
		defer turncancel()
		readBufLen := maxPayload
		if obfConn != nil {
			readBufLen = obfConn.MaxWire(maxPayload)
		}
		buf := make([]byte, readBufLen)
		for {
			n, _, err1 := relayConn.ReadFrom(buf)
			if err1 != nil {
				return
			}
			addr1 := internalPipeAddr.Load()
			if addr1 == nil {
				continue
			}

			if addr, ok := addr1.(net.Addr); ok {
				payload := buf[:n]
				if obfConn != nil {
					p, uErr := obfConn.UnwrapInPlace(buf[:n])
					if uErr != nil {
						deps.log().Errorf("[STREAM %d] OBF unwrap failed: %v (n=%d)", streamID, uErr, n)
						continue
					}
					payload = p
				}
				if params.TrafficStats != nil {
					params.TrafficStats.AddRx(len(payload))
				}
				if _, err := conn2.WriteTo(payload, addr); err != nil {
					return
				}
			}
		}
	})

	wg.Wait()
	if err := relayConn.SetDeadline(time.Time{}); err != nil {
		deps.log().Errorf("Failed to clear relay deadline: %s", err)
	}
}
