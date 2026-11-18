package mtglib

import (
	"context"
	"errors"
	"io"
	"net"
	"time"

	"github.com/9seconds/mtg/v2/essentials"
	"github.com/9seconds/mtg/v2/mtglib/obfuscation"
	"github.com/panjf2000/ants/v2"
)

var (
	// ErrIPBlocklisted is returned by ServeWebConn if the client IP is in the
	// blocklist.
	ErrIPBlocklisted = errors.New("ip is blocklisted")

	// ErrIPNotAllowlisted is returned by ServeWebConn if the client IP is not
	// in the allowlist.
	ErrIPNotAllowlisted = errors.New("ip is not allowlisted")

	// ErrConcurrencyLimited is returned by ServeWebConn if the worker pool is
	// exhausted.
	ErrConcurrencyLimited = errors.New("concurrency limit reached")

	// ErrSecretIndex is returned by ServeWebConn if the secret index does not
	// point to a configured secret.
	ErrSecretIndex = errors.New("secret index out of range")

	// ErrProxyClosed is returned by ServeWebConn if the proxy is shut down.
	ErrProxyClosed = errors.New("proxy is closed")

	// ErrRemoteAddr is returned by ServeWebConn if conn.RemoteAddr() is not
	// a *net.TCPAddr.
	ErrRemoteAddr = errors.New("remote address is not a TCP address")
)

// obfuscatedFrameSize is the length of the obfuscated2 client handshake.
const obfuscatedFrameSize = 64

// ServeWebConn admits an already-TLS-terminated connection whose first bytes
// are the obfuscated2 handshake. Non-blocking: it runs the same allowlist,
// blocklist and pool checks as Serve, then hands the conn to a worker.
//
// secretIdx is the index into the configured secrets that keys the obfuscated2
// handshake of this connection.
//
// conn.RemoteAddr() must be a non-nil *net.TCPAddr: the client IP is taken
// from it for the access lists and events. Otherwise ErrRemoteAddr is
// returned.
//
// On any returned error the connection has been closed.
func (p *Proxy) ServeWebConn(conn essentials.Conn, secretIdx int) error {
	if secretIdx < 0 || secretIdx >= len(p.secrets) {
		conn.Close() //nolint: errcheck
		return ErrSecretIndex
	}

	if p.ctx.Err() != nil {
		conn.Close() //nolint: errcheck
		return ErrProxyClosed
	}

	tcpAddr, ok := conn.RemoteAddr().(*net.TCPAddr)
	if !ok || tcpAddr == nil {
		conn.Close() //nolint: errcheck
		return ErrRemoteAddr
	}

	ipAddr := tcpAddr.IP
	logger := p.logger.BindStr("ip", ipAddr.String())

	if !p.allowlist.Contains(ipAddr) {
		conn.Close() //nolint: errcheck
		logger.Info("ip was rejected by allowlist")
		p.eventStream.Send(p.ctx, NewEventIPAllowlisted(ipAddr))

		return ErrIPNotAllowlisted
	}

	if p.blocklist.Contains(ipAddr) {
		conn.Close() //nolint: errcheck
		logger.Info("ip was blacklisted")
		p.eventStream.Send(p.ctx, NewEventIPBlocklisted(ipAddr))

		return ErrIPBlocklisted
	}

	// Counted before dispatch: ServeWebConn is called from foreign goroutines
	// that hold no count, so without this Shutdown could pass Wait while a
	// task is queued. The worker calls Done. admitMutex makes the check and the
	// Add atomic with respect to Shutdown's cancel, so after Shutdown starts
	// waiting no further Add can happen.
	p.admitMutex.RLock()

	if p.ctx.Err() != nil {
		p.admitMutex.RUnlock()
		conn.Close() //nolint: errcheck

		return ErrProxyClosed
	}

	p.streamWaitGroup.Add(1)
	p.admitMutex.RUnlock()

	err := p.workerPool.Invoke(poolTask{conn: conn, web: true, secretIdx: secretIdx})
	if err != nil {
		p.streamWaitGroup.Done()
	}

	switch {
	case err == nil:
		return nil
	case errors.Is(err, ants.ErrPoolOverload):
		conn.Close() //nolint: errcheck
		logger.Info("connection was concurrency limited")
		p.eventStream.Send(p.ctx, NewEventConcurrencyLimited())

		return ErrConcurrencyLimited
	default:
		conn.Close() //nolint: errcheck

		return ErrProxyClosed
	}
}

// serveWebConn is the worker body for ServeWebConn. TLS is already terminated
// by the caller, so Fake TLS and the doppelganger wrapper are skipped.
func (p *Proxy) serveWebConn(conn essentials.Conn, secretIdx int) {
	// Registered by ServeWebConn before dispatch, so Shutdown waits for workers
	// that are queued but not yet running.
	defer p.streamWaitGroup.Done()

	ctx := newStreamContext(p.ctx, p.logger, conn)
	defer ctx.Close()

	if err := ctx.clientConn.SetDeadline(time.Now().Add(p.handshakeTimeout)); err != nil {
		ctx.logger.WarningError("cannot set handshake timeout", err)
		return
	}

	stop := context.AfterFunc(ctx, func() {
		ctx.Close()
	})
	defer stop()

	p.eventStream.Send(ctx, NewEventStart(ctx.streamID, ctx.ClientIP()))
	ctx.logger.Info("Stream has been started")

	defer func() {
		p.eventStream.Send(ctx, NewEventFinish(ctx.streamID))
		ctx.logger.Info("Stream has been finished")
	}()

	rewind := newConnRewind(ctx.clientConn)

	frame := make([]byte, obfuscatedFrameSize)
	if _, err := io.ReadFull(rewind, frame); err != nil {
		ctx.logger.InfoError("cannot read obfuscated handshake", err)
		return
	}

	if p.antiReplayCache.SeenBefore(frame) {
		ctx.logger.Warning("replay attack has been detected!")
		p.eventStream.Send(p.ctx, NewEventReplayAttack(ctx.streamID))

		return
	}

	rewind.Rewind()

	obfuscator := obfuscation.Obfuscator{Secret: p.secrets[secretIdx].Key[:]}

	dcID, obfConn, err := obfuscator.ReadHandshake(rewind)
	if err != nil {
		ctx.logger.InfoError("obfuscated handshake is failed", err)
		return
	}

	ctx.dc = dcID
	ctx.setClientConn(obfConn)
	ctx.secretIdx = secretIdx
	ctx.logger = ctx.logger.BindInt("secret", secretIdx).BindInt("dc", dcID)
	p.eventStream.Send(ctx, NewEventClientMatched(ctx.streamID, secretIdx))

	p.serveAuthenticated(ctx)
}
