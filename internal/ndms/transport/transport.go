package transport

import (
	"net/http"
	"time"
)

// sharedTransport is the HTTP transport reused across all RCI Client
// instances. Settings mirror what the legacy rci.sharedTransport uses —
// modestly-sized keep-alive pool so we don't pay TCP handshake cost on
// every call, but capped to avoid leaking connections under bursts.
var sharedTransport = &http.Transport{
	MaxIdleConns:        50,
	MaxIdleConnsPerHost: 10,
	IdleConnTimeout:     90 * time.Second,
	DisableKeepAlives:   false,
}

// SharedTransport returns the shared http.Transport. Exposed for reuse
// by any consumer that needs its own *http.Client pointing at NDMS but
// wants to share the connection pool.
func SharedTransport() *http.Transport { return sharedTransport }
