// Package nwg provides a client for querying Keenetic's native WireGuard
// interface state via the RCI API (http://localhost:79).
// It replaces the awg CLI-based wg.Client for NativeWG backend state detection.
package nwg

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const rciBaseURL = "http://localhost:79/rci"

// neverHandshake is the sentinel value RCI uses when no handshake has occurred.
// It equals math.MaxInt32 (2^31 - 1).
const neverHandshake int64 = 2147483647

// RCIClient queries Keenetic RCI API for WireGuard interface state.
type RCIClient struct {
	http *http.Client
}

// NewRCIClient creates a new RCI client with sensible defaults.
func NewRCIClient() *RCIClient {
	return &RCIClient{
		http: &http.Client{Timeout: 10 * time.Second},
	}
}

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

// GetInterfaceState queries RCI for a Wireguard{N} interface and returns
// its parsed state. ndmsName must be the NDMS interface name (e.g. "Wireguard0").
func (c *RCIClient) GetInterfaceState(ctx context.Context, ndmsName string) (NWGState, error) {
	body, err := c.rciGetRaw(ctx, "/show/interface/"+ndmsName)
	if err != nil {
		return NWGState{}, fmt.Errorf("rci show interface %s: %w", ndmsName, err)
	}
	return parseRCIInterfaceResponse(body)
}

// ListWireguardInterfaces returns existing Wireguard interface names from RCI.
// It fetches all interfaces and filters by type == "Wireguard".
func (c *RCIClient) ListWireguardInterfaces(ctx context.Context) ([]string, error) {
	body, err := c.rciGetRaw(ctx, "/show/interface/")
	if err != nil {
		return nil, fmt.Errorf("rci show interface: %w", err)
	}
	return parseRCIInterfaceList(body)
}

// ListWireguardInterfaceInfos returns Wireguard interfaces with descriptions.
func (c *RCIClient) ListWireguardInterfaceInfos(ctx context.Context) ([]WGInterfaceInfo, error) {
	body, err := c.rciGetRaw(ctx, "/show/interface/")
	if err != nil {
		return nil, fmt.Errorf("rci show interface: %w", err)
	}
	return parseRCIInterfaceInfoList(body)
}

// rciGetRaw fetches raw bytes from an RCI endpoint.
func (c *RCIClient) rciGetRaw(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", rciBaseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("rci %s: %w", path, err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rci %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rci %s: status %d", path, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("rci %s: read body: %w", path, err)
	}
	return body, nil
}

// --- internal JSON structures matching RCI response format ---

// rciWGInterface represents a single WireGuard interface from RCI.
// Example response for /show/interface/Wireguard0:
//
//	{
//	  "id": "Wireguard0",
//	  "type": "Wireguard",
//	  "link": "down",
//	  "wireguard": {
//	    "status": "down",
//	    "peer": [{
//	      "online": false,
//	      "last-handshake": 2147483647,
//	      "rxbytes": 0,
//	      "txbytes": 0
//	    }]
//	  },
//	  "summary": { "layer": { "conf": "disabled" } }
//	}
type rciWGInterface struct {
	ID          string             `json:"id"`
	Type        string             `json:"type"`
	Description string             `json:"description"`
	Link        string             `json:"link"`
	Connected   json.RawMessage    `json:"connected"`
	Uptime      int64              `json:"uptime"`
	WireGuard   *rciWGSection      `json:"wireguard"`
	Summary     rciWGSummary       `json:"summary"`
}

// WGInterfaceInfo holds basic info about a Wireguard interface.
type WGInterfaceInfo struct {
	Name        string
	Description string
}

type rciWGSection struct {
	Status string       `json:"status"`
	Peer   []rciWGPeer  `json:"peer"`
}

type rciWGPeer struct {
	Online        bool   `json:"online"`
	LastHandshake int64  `json:"last-handshake"`
	RxBytes       int64  `json:"rxbytes"`
	TxBytes       int64  `json:"txbytes"`
	Via           string `json:"via"` // NDMS WAN name (e.g. "PPPoE0"), empty = auto
}

type rciWGSummary struct {
	Layer struct {
		Conf string `json:"conf"`
	} `json:"layer"`
}

// --- parsing functions (unexported, testable within package) ---

// parseRCIInterfaceResponse parses a raw RCI JSON response for a single
// Wireguard interface into NWGState.
func parseRCIInterfaceResponse(data []byte) (NWGState, error) {
	var iface rciWGInterface
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
//   - a JSON number (unix timestamp, e.g. 1741330257) → convert to ISO 8601
//   - a JSON string "yes"/"no" (OpkgTun-style boolean) → ignore
//   - missing/null → empty
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
	var allIfaces map[string]rciWGInterface
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

// parseRCIInterfaceInfoList is like parseRCIInterfaceList but also returns descriptions.
func parseRCIInterfaceInfoList(data []byte) ([]WGInterfaceInfo, error) {
	var allIfaces map[string]rciWGInterface
	if err := json.Unmarshal(data, &allIfaces); err != nil {
		return nil, fmt.Errorf("decode rci interface list: %w", err)
	}

	var result []WGInterfaceInfo
	for _, iface := range allIfaces {
		if strings.EqualFold(iface.Type, "Wireguard") {
			result = append(result, WGInterfaceInfo{
				Name:        iface.ID,
				Description: iface.Description,
			})
		}
	}
	return result, nil
}
