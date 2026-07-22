// Package udpserver реализует серверную UDP-ретрансляцию: DTLS-терминированный
// поток форвардится к UDP-backend (WireGuard) и обратно.
package udpserver

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/logx"
)

const (
	udpRelayBufSize = 1600
	udpIdleTimeout  = 30 * time.Minute
)

// Handle форвардит DTLS-пакеты между conn и UDP-backend на connectAddr
// до закрытия любой стороны. Блокируется до выхода обеих copy-горутин.
func Handle(ctx context.Context, logger logx.Logger, conn net.Conn, connectAddr string) {
	serverConn, err := (&net.Dialer{}).DialContext(ctx, "udp", connectAddr)
	if err != nil {
		logger.Errorf("udpserver: dial backend: %v", err)
		return
	}
	defer func() {
		if err = serverConn.Close(); err != nil {
			logger.Errorf("udpserver: close outgoing connection: %s", err)
		}
	}()

	ctx2, cancel := context.WithCancel(ctx)
	defer cancel()

	context.AfterFunc(ctx2, func() {
		if err := conn.SetDeadline(time.Now()); err != nil {
			logger.Errorf("udpserver: set incoming deadline: %s", err)
		}
		if err := serverConn.SetDeadline(time.Now()); err != nil {
			logger.Errorf("udpserver: set outgoing deadline: %s", err)
		}
	})

	var wg sync.WaitGroup
	wg.Go(func() {
		defer cancel()
		copyOne(ctx2, logger, conn, serverConn)
	})
	wg.Go(func() {
		defer cancel()
		copyOne(ctx2, logger, serverConn, conn)
	})
	wg.Wait()
}

// copyOne читает из src и пишет в dst до отмены ctx или ошибки любой стороны.
// Каждый read/write сбрасывает idle timeout - зависший конец закрывается, а не висит.
func copyOne(ctx context.Context, logger logx.Logger, src, dst net.Conn) {
	buf := make([]byte, udpRelayBufSize)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if err := src.SetReadDeadline(time.Now().Add(udpIdleTimeout)); err != nil {
			logger.Errorf("udpserver: set read deadline: %s", err)
			return
		}
		n, err := src.Read(buf)
		if err != nil {
			logger.Debugf("udpserver: read: %s", err)
			return
		}
		if werr := dst.SetWriteDeadline(time.Now().Add(udpIdleTimeout)); werr != nil {
			logger.Errorf("udpserver: set write deadline: %s", werr)
			return
		}
		if _, werr := dst.Write(buf[:n]); werr != nil {
			logger.Debugf("udpserver: write: %s", werr)
			return
		}
	}
}
