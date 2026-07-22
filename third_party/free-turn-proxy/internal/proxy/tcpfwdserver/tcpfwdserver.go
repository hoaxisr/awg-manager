// Package tcpfwdserver реализует серверную VLESS lane: KCP+smux поверх
// DTLS-соединения, каждый smux-поток форвардится как TCP-соединение к backend
// (Xray/VLESS). Bond-потоки автоопределяются по magic-префиксу и диспетчеризуются
// в bondserver.Registry.
package tcpfwdserver

import (
	"context"
	"io"
	"net"
	"sync"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/logx"
	"github.com/samosvalishe/free-turn-proxy/internal/netconn"
	"github.com/samosvalishe/free-turn-proxy/internal/proxy/bondserver"
	"github.com/samosvalishe/free-turn-proxy/internal/transport/kcptun"
	"github.com/samosvalishe/free-turn-proxy/internal/wire/bondframe"
	"github.com/xtaci/smux"
)

// Handle оборачивает dtlsConn в KCP+smux и форвардит каждый принятый поток как
// TCP-соединение к connectAddr. Потоки, чьи первые 4 байта совпадают с bond magic,
// передаются в registry.
func Handle(ctx context.Context, logger logx.Logger, registry *bondserver.Registry, dtlsConn net.Conn, connectAddr string, kcpProfile kcptun.Profile, kcpFEC kcptun.FEC) {
	kcpSess, err := kcptun.NewKCPOverDTLS(dtlsConn, true, kcpProfile, kcpFEC)
	if err != nil {
		logger.Errorf("tcpfwdserver: KCP session: %s", err)
		return
	}
	defer func() {
		if closeErr := kcpSess.Close(); closeErr != nil {
			logger.Errorf("tcpfwdserver: close KCP session: %v", closeErr)
		}
	}()
	logger.Debugf("KCP session established (server)")

	smuxSess, err := smux.Server(kcpSess, kcptun.DefaultSmuxConfig())
	if err != nil {
		logger.Errorf("tcpfwdserver: smux server: %s", err)
		return
	}
	defer func() {
		if err := smuxSess.Close(); err != nil {
			logger.Errorf("tcpfwdserver: close smux session: %v", err)
		}
	}()
	logger.Debugf("smux session established (server)")

	var wg sync.WaitGroup
	for {
		stream, err := smuxSess.AcceptStream()
		if err != nil {
			select {
			case <-ctx.Done():
			default:
				logger.Errorf("tcpfwdserver: smux accept: %s", err)
			}
			break
		}

		s := stream
		wg.Go(func() {
			handleStream(ctx, logger, registry, s, connectAddr)
		})
	}
	wg.Wait()
}

func handleStream(ctx context.Context, logger logx.Logger, registry *bondserver.Registry, s *smux.Stream, connectAddr string) {
	var prefix [4]byte
	if _, err := io.ReadFull(s, prefix[:]); err != nil {
		if err != io.EOF && err != io.ErrUnexpectedEOF {
			logger.Errorf("tcpfwdserver: smux stream prefix read: %v", err)
		}
		_ = s.Close()
		return
	}
	if string(prefix[:]) == bondframe.Magic {
		logger.Debugf("auto-detected bond smux stream")
		registry.HandleStreamAfterMagic(ctx, s, connectAddr, prefix)
		return
	}

	defer func() {
		if err := s.Close(); err != nil && err != smux.ErrGoAway {
			logger.Errorf("tcpfwdserver: close smux stream: %v", err)
		}
	}()

	backendConn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", connectAddr)
	if err != nil {
		logger.Errorf("tcpfwdserver: backend dial: %s", err)
		return
	}
	defer func() {
		if err := backendConn.Close(); err != nil {
			logger.Errorf("tcpfwdserver: close backend connection: %v", err)
		}
	}()

	netconn.BiCopy(ctx, &prefixedConn{Conn: s, prefix: prefix[:]}, backendConn, logger.Debugf)
}

// prefixedConn повторно вставляет magic-peek prefix при первых чтениях,
// чтобы backend видел полный оригинальный поток байт.
type prefixedConn struct {
	net.Conn
	prefix []byte
}

func (c *prefixedConn) Read(p []byte) (int, error) {
	if len(c.prefix) > 0 {
		n := copy(p, c.prefix)
		c.prefix = c.prefix[n:]
		return n, nil
	}
	return c.Conn.Read(p)
}
