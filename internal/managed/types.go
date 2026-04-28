package managed

// CreateServerRequest contains parameters for creating a managed WireGuard server.
type CreateServerRequest struct {
	Address     string `json:"address"`               // e.g. "10.0.0.1"
	Mask        string `json:"mask"`                  // e.g. "24" or "255.255.255.0"
	ListenPort  int    `json:"listenPort"`            // 1-65535
	Description string `json:"description,omitempty"` // user-facing display name; defaults to ManagedServerDescription if empty
	Endpoint    string `json:"endpoint,omitempty"`    // custom endpoint (IP or domain)
	DNS         string `json:"dns,omitempty"`         // custom DNS for client configs
	MTU         int    `json:"mtu,omitempty"`         // custom MTU for client configs
}

// UpdateServerRequest contains parameters for updating the managed server.
//
// Description, Endpoint, DNS, and MTU are pointers so callers can distinguish
// "absent" (preserve existing) from "explicit set" (including empty/zero).
//
// Semantics: nil pointer = preserve, non-nil pointer = set to that value.
// Sending an empty string or zero value via a non-nil pointer DOES clear the
// stored field. This is the cleanest way to support clearing without
// inventing a sentinel value, and it matches what the frontend can express.
type UpdateServerRequest struct {
	Address     string  `json:"address"`
	Mask        string  `json:"mask"`
	ListenPort  int     `json:"listenPort"`
	Description *string `json:"description,omitempty"`
	Endpoint    *string `json:"endpoint,omitempty"`
	DNS         *string `json:"dns,omitempty"`
	MTU         *int    `json:"mtu,omitempty"`
}

// AddPeerRequest contains parameters for adding a peer to the managed server.
type AddPeerRequest struct {
	Description string `json:"description"`
	TunnelIP    string `json:"tunnelIP"` // e.g. "10.0.0.2/32"
	DNS         string `json:"dns,omitempty"`
}

// UpdatePeerRequest contains parameters for updating a peer.
type UpdatePeerRequest struct {
	Description string `json:"description"`
	TunnelIP    string `json:"tunnelIP"`
	DNS         string `json:"dns,omitempty"`
}

// TogglePeerRequest contains parameters for enabling/disabling a peer.
type TogglePeerRequest struct {
	PublicKey string `json:"publicKey"`
	Enabled   bool   `json:"enabled"`
}

// ManagedServerStats holds runtime statistics for the managed server.
type ManagedServerStats struct {
	Status string            `json:"status"` // "up" or "down"
	Peers  []ManagedPeerStats `json:"peers"`
}

// ManagedPeerStats holds runtime statistics for a single peer.
type ManagedPeerStats struct {
	PublicKey     string `json:"publicKey"`
	Endpoint      string `json:"endpoint"`
	RxBytes       int64  `json:"rxBytes"`
	TxBytes       int64  `json:"txBytes"`
	LastHandshake string `json:"lastHandshake"`
	Online        bool   `json:"online"`
}

// ManagedServerDescription is the NDMS description for our managed server.
const ManagedServerDescription = "AWGM WG Server"

// PolicyOption is the dropdown-friendly representation of an IP Policy
// profile fetched from the router. Surfaces both the stable id (sent
// to RCI) and the user-facing description.
type PolicyOption struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}
