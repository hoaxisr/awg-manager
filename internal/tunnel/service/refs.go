// internal/tunnel/service/refs.go
package service

import (
	"fmt"
	"strings"
)

// ErrTunnelReferenced is returned by Delete when the tunnel is
// referenced by deviceproxy or any sing-box-router rule. Callers
// (API handlers) typecheck this and translate to HTTP 409 with
// structured details so the UI can deeplink the user to the
// referencing config.
type ErrTunnelReferenced struct {
	TunnelID    string
	DeviceProxy bool
	RouterRules []int
}

func (e ErrTunnelReferenced) Error() string {
	parts := []string{}
	if e.DeviceProxy {
		parts = append(parts, "device-proxy selector")
	}
	if len(e.RouterRules) > 0 {
		parts = append(parts, fmt.Sprintf("%d router rule(s)", len(e.RouterRules)))
	}
	return "tunnel " + e.TunnelID + " is referenced by: " + strings.Join(parts, ", ")
}

// DeviceProxyRefChecker reports whether tag is currently in the
// deviceproxy selector members or set as the persisted SelectedOutbound.
type DeviceProxyRefChecker interface {
	HasSelectorReference(tag string) bool
}

// RouterRefChecker returns the indices of router rules whose outbound
// equals tag. Empty slice = no references.
type RouterRefChecker interface {
	RulesReferencing(tag string) []int
}

// checkTunnelReferences returns ErrTunnelReferenced if any checker
// reports references to the tunnel's awg-{id} tag, nil otherwise.
// Nil checkers are treated as "no references" (degrades safely
// when wiring isn't fully done in tests).
func checkTunnelReferences(tunnelID string, dp DeviceProxyRefChecker, r RouterRefChecker) error {
	tag := "awg-" + tunnelID
	refs := ErrTunnelReferenced{TunnelID: tunnelID}
	refused := false
	if dp != nil && dp.HasSelectorReference(tag) {
		refs.DeviceProxy = true
		refused = true
	}
	if r != nil {
		if rules := r.RulesReferencing(tag); len(rules) > 0 {
			refs.RouterRules = rules
			refused = true
		}
	}
	if refused {
		return refs
	}
	return nil
}
