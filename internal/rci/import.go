package rci

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// ImportWireguardConfig uploads a .conf file to NDMS.
// confData is the raw .conf file content (NOT base64).
// Returns the NDMS interface name (e.g. "Wireguard1").
func (c *Client) ImportWireguardConfig(ctx context.Context, confData []byte, filename string) (string, error) {
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
	resp, err := c.Post(ctx, payload)
	if err != nil {
		return "", fmt.Errorf("import wireguard config: %w", err)
	}

	// Real NDMS response format (verified on device):
	// {"interface":{"wireguard":{"import":{"created":"Wireguard0","intersects":"","status":[...]}}}}
	var result struct {
		Interface struct {
			Wireguard struct {
				Import struct {
					Created string `json:"created"`
				} `json:"import"`
			} `json:"wireguard"`
		} `json:"interface"`
	}
	if err := json.Unmarshal(resp, &result); err != nil {
		return "", fmt.Errorf("import: parse response: %w", err)
	}
	name := result.Interface.Wireguard.Import.Created
	if name == "" {
		return "", fmt.Errorf("import: no interface name in response")
	}
	return name, nil
}
