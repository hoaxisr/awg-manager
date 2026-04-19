package nwg

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/rci"
)

// neverHandshake is the sentinel value RCI uses when no handshake has occurred.
// It equals math.MaxInt32 (2^31 - 1).
const neverHandshake int64 = rci.NeverHandshake

// NWGState holds parsed state from an RCI response for a single
// Wireguard interface.
type NWGState struct {
	Exists        bool
	ConfLayer     string // "running" | "disabled"
	LinkUp        bool
	WGStatus      string // "up" | "down"
	PeerOnline    bool
	LastHandshake int64  // unix timestamp, 2147483647 = never
	RxBytes       int64
	TxBytes       int64
	PeerVia       string // NDMS WAN name from peer "via" field (e.g. "PPPoE0")
	Connected     string // RFC3339 timestamp converted from NDMS "connected" field (unix ts or string)
}

// parseRCIInterfaceResponse parses a raw RCI JSON response for a single
// Wireguard interface into NWGState.
func parseRCIInterfaceResponse(data []byte) (NWGState, error) {
	var iface rci.WGInterface
	if err := json.Unmarshal(data, &iface); err != nil {
		return NWGState{}, fmt.Errorf("decode rci interface: %w", err)
	}

	// If the response has no id, the interface was not found.
	// RCI returns {} or {"error": ...} for missing interfaces.
	if iface.ID == "" {
		return NWGState{Exists: false}, nil
	}

	connectedAt := parseConnectedField(iface.Connected)
	// Fallback: compute from uptime (seconds since interface came up)
	if connectedAt == "" && iface.Uptime > 0 {
		connectedAt = time.Now().Add(-time.Duration(iface.Uptime) * time.Second).UTC().Format(time.RFC3339)
	}

	state := NWGState{
		Exists:    true,
		ConfLayer: iface.Summary.Layer.Conf,
		LinkUp:    iface.Link == "up",
		Connected: connectedAt,
	}

	if iface.WireGuard != nil {
		state.WGStatus = iface.WireGuard.Status

		if len(iface.WireGuard.Peer) > 0 {
			peer := iface.WireGuard.Peer[0]
			state.PeerOnline = peer.Online
			state.LastHandshake = peer.LastHandshake
			state.RxBytes = peer.RxBytes
			state.TxBytes = peer.TxBytes
			state.PeerVia = peer.Via
		}
	}

	return state, nil
}

// parseConnectedField interprets the NDMS "connected" field which can be:
//   - a JSON number (unix timestamp, e.g. 1741330257) -> convert to ISO 8601
//   - a JSON string "yes"/"no" (OpkgTun-style boolean) -> ignore
//   - missing/null -> empty
func parseConnectedField(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	// Try number (unix timestamp)
	s := strings.TrimSpace(string(raw))
	if s[0] >= '0' && s[0] <= '9' {
		ts, err := strconv.ParseInt(s, 10, 64)
		if err == nil && ts > 0 {
			return time.Unix(ts, 0).UTC().Format(time.RFC3339)
		}
	}
	// Try quoted string
	var str string
	if json.Unmarshal(raw, &str) == nil {
		// "yes"/"no" are not timestamps
		if str == "yes" || str == "no" || str == "" {
			return ""
		}
		// Could be a numeric string
		ts, err := strconv.ParseInt(str, 10, 64)
		if err == nil && ts > 0 {
			return time.Unix(ts, 0).UTC().Format(time.RFC3339)
		}
		// Already ISO format?
		if _, err := time.Parse(time.RFC3339, str); err == nil {
			return str
		}
	}
	return ""
}

// parseRCIInterfaceList parses the raw RCI JSON response from /show/interface/
// which returns a map of interface objects keyed by interface ID.
// It filters by type == "Wireguard" and returns matching interface names.
func parseRCIInterfaceList(data []byte) ([]string, error) {
	var allIfaces map[string]rci.WGInterface
	if err := json.Unmarshal(data, &allIfaces); err != nil {
		return nil, fmt.Errorf("decode rci interface list: %w", err)
	}

	var names []string
	for _, iface := range allIfaces {
		if strings.EqualFold(iface.Type, "Wireguard") {
			names = append(names, iface.ID)
		}
	}
	return names, nil
}

