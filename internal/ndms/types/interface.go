package types

import "encoding/json"

// WGInterface represents a WireGuard interface from /show/interface/{name}.
// Consumed by the NWG operator state parser and diagnostics collectors —
// both need the combined interface state + first-peer fields in a single
// HTTP roundtrip, which is why this raw shape is preserved instead of going
// through the typed query layer (which would split into two RCI calls).
type WGInterface struct {
	ID          string          `json:"id"`
	Type        string          `json:"type"`
	Description string          `json:"description"`
	Link        string          `json:"link"`
	Connected   json.RawMessage `json:"connected"`
	Uptime      int64           `json:"uptime"`
	WireGuard   *WGSection      `json:"wireguard"`
	Summary     WGSummary       `json:"summary"`
}

// WGSection holds WireGuard-specific data nested under WGInterface.
type WGSection struct {
	Status string   `json:"status"`
	Peer   []WGPeer `json:"peer"`
}

// WGPeer is a single WireGuard peer entry under WGSection.
type WGPeer struct {
	Online        bool   `json:"online"`
	LastHandshake int64  `json:"last-handshake"`
	RxBytes       int64  `json:"rxbytes"`
	TxBytes       int64  `json:"txbytes"`
	Via           string `json:"via"`
}

// WGSummary holds the layer summary nested under WGInterface.
type WGSummary struct {
	Layer struct {
		Conf string `json:"conf"`
	} `json:"layer"`
}

// NeverHandshake is the sentinel value RCI returns when no handshake has
// occurred yet on a peer.
const NeverHandshake int64 = 2147483647
