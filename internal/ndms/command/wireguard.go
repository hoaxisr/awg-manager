package command

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

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
		func() { c.queries.Interfaces.Invalidate(name) },
		c.queries.RunningConfig.InvalidateAll)
}

// ImportWireguardConfig uploads a .conf file to NDMS and returns the
// NDMS interface name created from the import (e.g. "Wireguard1").
// confData is the raw .conf body (NOT base64 — encoded internally).
func (c *WireguardCommands) ImportWireguardConfig(ctx context.Context, confData []byte, filename string) (string, error) {
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
		return "", fmt.Errorf("import wireguard: %w", err)
	}

	// Real NDMS response shape:
	// {"interface":{"wireguard":{"import":{"created":"Wireguard0",...}}}}
	var parsed struct {
		Interface struct {
			Wireguard struct {
				Import struct {
					Created string `json:"created"`
				} `json:"import"`
			} `json:"wireguard"`
		} `json:"interface"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil {
		return "", fmt.Errorf("import wireguard: decode: %w", err)
	}
	if parsed.Interface.Wireguard.Import.Created == "" {
		return "", fmt.Errorf("import wireguard: empty created field in response")
	}
	return parsed.Interface.Wireguard.Import.Created, nil
}
