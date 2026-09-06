// Package clientip extracts the peer address of an HTTP request for
// throttling and audit logs. It is a leaf package so both the web login
// (internal/api) and the MCP key middleware (internal/mcp, which must stay
// free of daemon dependencies) share one definition.
package clientip

import (
	"net"
	"net/http"
)

// FromRequest returns the IP part of r.RemoteAddr. X-Forwarded-For is
// deliberately ignored: the daemon serves the LAN directly, and the only
// proxy in front of it (KeenDNS on the router itself) is not one whose
// headers a LAN client could not forge. Honouring the header would let any
// client pick its own throttle bucket.
func FromRequest(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
