package mtglib

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net"
	"sync"
	"time"

	"github.com/9seconds/mtg/v2/essentials"
)

type streamContext struct {
	ctx       context.Context
	ctxCancel context.CancelFunc

	// connMutex guards clientConn and telegramConn. ServeConn replaces them as
	// it layers doppelganger, FakeTLS, obfuscation and the Telegram dial on
	// top of each other, while Close can run concurrently on another goroutine
	// the moment the context is cancelled — which Proxy.Shutdown does. An
	// interface value is two words, so an unsynchronised read during that swap
	// can pair one conn's type with another's data.
	connMutex    sync.Mutex
	clientConn   essentials.Conn
	telegramConn essentials.Conn

	streamID string
	dc       int
	logger   Logger

	// secretIdx is the index into Proxy.secrets of the secret that
	// authenticated this stream, or -1 before the FakeTLS handshake has
	// matched one. Domain-fronted streams keep -1 for their whole life.
	secretIdx int
}

func (s *streamContext) Deadline() (time.Time, bool) {
	return s.ctx.Deadline()
}

func (s *streamContext) Done() <-chan struct{} {
	return s.ctx.Done()
}

func (s *streamContext) Err() error {
	return s.ctx.Err() //nolint: wrapcheck
}

func (s *streamContext) Value(key any) any {
	return s.ctx.Value(key)
}

func (s *streamContext) Close() {
	s.ctxCancel()

	s.connMutex.Lock()
	defer s.connMutex.Unlock()

	if s.clientConn != nil {
		s.clientConn.Close() //nolint: errcheck
	}

	if s.telegramConn != nil {
		s.telegramConn.Close() //nolint: errcheck
	}
}

// setClientConn swaps in the next layer of the client connection.
func (s *streamContext) setClientConn(conn essentials.Conn) {
	s.connMutex.Lock()
	defer s.connMutex.Unlock()

	s.clientConn = conn
}

// setTelegramConn records the upstream connection once it is established.
func (s *streamContext) setTelegramConn(conn essentials.Conn) {
	s.connMutex.Lock()
	defer s.connMutex.Unlock()

	s.telegramConn = conn
}

func (s *streamContext) ClientIP() net.IP {
	return s.clientConn.RemoteAddr().(*net.TCPAddr).IP //nolint: forcetypeassert
}

func newStreamContext(ctx context.Context, logger Logger, clientConn essentials.Conn) *streamContext {
	connIDBytes := make([]byte, ConnectionIDBytesLength)

	if _, err := rand.Read(connIDBytes); err != nil {
		panic(err)
	}

	ctx, cancel := context.WithCancel(ctx)
	streamCtx := &streamContext{
		ctx:        ctx,
		ctxCancel:  cancel,
		clientConn: clientConn,
		streamID:   base64.RawURLEncoding.EncodeToString(connIDBytes),
		secretIdx:  -1,
	}
	streamCtx.logger = logger.
		BindStr("stream-id", streamCtx.streamID).
		BindStr("client-ip", streamCtx.ClientIP().String())

	return streamCtx
}
