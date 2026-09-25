package transport

import (
	"net/http"
	"time"
)

// baseTransport is the HTTP transport reused across all RCI Client
// instances (under the token layer, see token.go). Pool size synchronised with cap=30 default of the NDMS
// concurrency semaphore (cmd/awg-manager/main.go): MaxIdleConnsPerHost
// must exceed cap so that the 30 simultaneous in-flight requests never
// pay TCP-handshake cost on a hot keep-alive pool.
var baseTransport = &http.Transport{
	MaxIdleConns:        64,
	MaxIdleConnsPerHost: 50,
	IdleConnTimeout:     90 * time.Second,
	DisableKeepAlives:   false,
}

// sharedTransport — baseTransport с токеном X-NDMA-TKN (5.2+).
var sharedTransport http.RoundTripper = tokens

// SharedTransport отдаёт транспорт RCI для тех, кто строит свой http.Client
// к 127.0.0.1:79 мимо Client, — чтобы и они ходили с токеном.
func SharedTransport() http.RoundTripper { return sharedTransport }
