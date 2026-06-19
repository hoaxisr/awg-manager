package command

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

type WireguardCommands struct {
	poster  Poster
	save    *SaveCoordinator
	queries *query.Queries
}

func NewWireguardCommands(p Poster, s *SaveCoordinator, q *query.Queries) *WireguardCommands {
	return &WireguardCommands{poster: p, save: s, queries: q}
}

// SetASCParams sets the AmneziaWG ASC obfuscation parameters. The params
// json.RawMessage must be a JSON object with string values for
// jc/jmin/jmax/s1/s2 and hex strings for h1/h2/h3/h4 (OS ≥ 5.1 adds
// s3/s4/i1-i5). Caller is responsible for firmware-appropriate field set.
func (c *WireguardCommands) SetASCParams(ctx context.Context, name string, params json.RawMessage) error {
	var asc map[string]any
	if err := json.Unmarshal(params, &asc); err != nil {
		return fmt.Errorf("set asc params %s: parse: %w", name, err)
	}
	payload := map[string]any{
		"interface": map[string]any{
			name: map[string]any{
				"wireguard": map[string]any{"asc": asc},
			},
		},
	}
	return postMutation(ctx, c.poster, c.save, payload, "set asc params "+name,
		func() {
			c.queries.Interfaces.Invalidate(name)
			if c.queries.WGServers != nil {
				c.queries.WGServers.Invalidate(name)
			}
		},
		c.queries.RunningConfig.InvalidateAll)
}

// ImportResult holds the parsed outcome of a wireguard config import.
// Intersects names a pre-existing interface the imported config collides
// with (empty if none); Messages are the human-readable status[] lines the
// router returned. Both are kept so the caller does not lose context even
// on a successful import.
type ImportResult struct {
	Created    string
	Intersects string
	Messages   []string
}

// ImportWireguardConfig uploads a .conf file to NDMS and returns the import
// result, including the created NDMS interface name (e.g. "Wireguard1").
// confData is the raw .conf body (NOT base64 — encoded internally).
func (c *WireguardCommands) ImportWireguardConfig(ctx context.Context, confData []byte, filename string) (ImportResult, error) {
	encoded := base64.StdEncoding.EncodeToString(confData)
	payload := map[string]any{
		"interface": map[string]any{
			"wireguard": map[string]any{
				"import":   encoded,
				"name":     "",
				"filename": filename,
			},
		},
	}
	resp, err := c.poster.Post(ctx, payload)
	if err != nil {
		return ImportResult{}, fmt.Errorf("import wireguard: %w", err)
	}

	// Real NDMS response shape (captured on 5.01.A.x):
	// {"interface":{"wireguard":{"import":{
	//   "intersects":"", "created":"Wireguard3",
	//   "status":[{"status":"message","code":"...","ident":"...","message":"..."}]}}}}
	var parsed struct {
		Interface struct {
			Wireguard struct {
				Import struct {
					Intersects string `json:"intersects"`
					Created    string `json:"created"`
					Status     []struct {
						Status  string `json:"status"`
						Message string `json:"message"`
					} `json:"status"`
				} `json:"import"`
			} `json:"wireguard"`
		} `json:"interface"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil {
		return ImportResult{}, fmt.Errorf("import wireguard: decode: %w", err)
	}
	imp := parsed.Interface.Wireguard.Import

	var msgs []string
	for _, s := range imp.Status {
		if s.Message != "" {
			msgs = append(msgs, s.Message)
		}
	}

	if imp.Created == "" {
		// The router accepted the request (HTTP 200) but did not return a
		// created interface. The reason lives in the nested status[] array,
		// which the top-level error envelope check does not inspect — surface
		// it instead of an opaque message.
		detail := strings.Join(msgs, "; ")
		if detail == "" {
			detail = "no status message"
		}
		return ImportResult{}, fmt.Errorf("import wireguard: router returned no created interface (intersects=%q; status: %s)", imp.Intersects, detail)
	}
	return ImportResult{Created: imp.Created, Intersects: imp.Intersects, Messages: msgs}, nil
}

func (c *WireguardCommands) invalidateServer(name string) {
	if c.queries == nil {
		return
	}
	if c.queries.Interfaces != nil {
		c.queries.Interfaces.Invalidate(name)
	}
	if c.queries.WGServers != nil {
		c.queries.WGServers.Invalidate(name)
	}
	if c.queries.RunningConfig != nil {
		c.queries.RunningConfig.InvalidateAll()
	}
}

// AddPeer adds a peer to a WireGuard server interface.
func (c *WireguardCommands) AddPeer(ctx context.Context, ifaceName, pubKey, psk, comment, peerIP string, enabled bool) error {
	peer := map[string]any{
		"key":           pubKey,
		"preshared-key": psk,
		"connect":       enabled,
		"allow-ips": []map[string]any{
			{"address": peerIP, "mask": "255.255.255.255"},
		},
	}
	if comment != "" {
		peer["comment"] = comment
	}
	payload := map[string]any{
		"interface": map[string]any{
			ifaceName: map[string]any{
				"wireguard": map[string]any{
					"peer": []map[string]any{peer},
				},
			},
		},
	}
	return postMutationChecked(ctx, c.poster, c.save, payload, "add peer "+ifaceName,
		func() { c.invalidateServer(ifaceName) })
}

// RemovePeer removes a peer by public key.
func (c *WireguardCommands) RemovePeer(ctx context.Context, ifaceName, pubKey string) error {
	payload := map[string]any{
		"interface": map[string]any{
			ifaceName: map[string]any{
				"wireguard": map[string]any{
					"peer": []map[string]any{
						{"no": true, "key": pubKey},
					},
				},
			},
		},
	}
	return postMutationChecked(ctx, c.poster, c.save, payload, "remove peer "+ifaceName,
		func() { c.invalidateServer(ifaceName) })
}

// SetPeerConnect enables or disables a peer. comment must carry the peer's
// current name: a partial peer update without it makes NDMS wipe the stored
// comment (psk/allow-ips survive, comment does not).
func (c *WireguardCommands) SetPeerConnect(ctx context.Context, ifaceName, pubKey string, connect bool, comment string) error {
	peer := map[string]any{"key": pubKey, "connect": connect}
	if comment != "" {
		peer["comment"] = comment
	}
	payload := map[string]any{
		"interface": map[string]any{
			ifaceName: map[string]any{
				"wireguard": map[string]any{
					"peer": []map[string]any{peer},
				},
			},
		},
	}
	return postMutationChecked(ctx, c.poster, c.save, payload, "toggle peer "+ifaceName,
		func() { c.invalidateServer(ifaceName) })
}

// SetPeerComment sets the description/comment for a peer.
func (c *WireguardCommands) SetPeerComment(ctx context.Context, ifaceName, pubKey, comment string) error {
	payload := map[string]any{
		"interface": map[string]any{
			ifaceName: map[string]any{
				"wireguard": map[string]any{
					"peer": []map[string]any{
						{"key": pubKey, "comment": comment},
					},
				},
			},
		},
	}
	return postMutationChecked(ctx, c.poster, c.save, payload, "rename peer "+ifaceName,
		func() { c.invalidateServer(ifaceName) })
}

// UpdatePeerAllowIPs removes old /32 and sets a new one.
func (c *WireguardCommands) UpdatePeerAllowIPs(ctx context.Context, ifaceName, pubKey, oldIP, newIP string) error {
	if oldIP != "" {
		payload := map[string]any{
			"interface": map[string]any{
				ifaceName: map[string]any{
					"wireguard": map[string]any{
						"peer": []map[string]any{
							{
								"key": pubKey,
								"allow-ips": []map[string]any{
									{"no": true, "address": oldIP, "mask": "255.255.255.255"},
								},
							},
						},
					},
				},
			},
		}
		if err := postMutationChecked(ctx, c.poster, c.save, payload, "remove peer allow-ips "+ifaceName,
			func() { c.invalidateServer(ifaceName) }); err != nil {
			return fmt.Errorf("remove old allow-ips: %w", err)
		}
	}
	payload := map[string]any{
		"interface": map[string]any{
			ifaceName: map[string]any{
				"wireguard": map[string]any{
					"peer": []map[string]any{
						{
							"key": pubKey,
							"allow-ips": []map[string]any{
								{"address": newIP, "mask": "255.255.255.255"},
							},
						},
					},
				},
			},
		},
	}
	return postMutationChecked(ctx, c.poster, c.save, payload, "set peer allow-ips "+ifaceName,
		func() { c.invalidateServer(ifaceName) })
}
