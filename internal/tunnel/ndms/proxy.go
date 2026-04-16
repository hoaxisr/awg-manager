package ndms

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
)

// CreateProxy creates a Proxy interface in NDMS with a SOCKS5 upstream.
// Brings the interface up immediately after creation.
func (c *ClientImpl) CreateProxy(ctx context.Context, name, description, upstreamHost string, upstreamPort int, socks5UDP bool) error {
	payload := buildProxyCreatePayload(name, description, upstreamHost, upstreamPort, socks5UDP)
	if _, err := c.rci.Post(ctx, payload); err != nil {
		return fmt.Errorf("create proxy %s: %w", name, err)
	}
	return nil
}

// DeleteProxy removes a Proxy interface from NDMS.
func (c *ClientImpl) DeleteProxy(ctx context.Context, name string) error {
	payload := map[string]any{
		"interface": map[string]any{
			name: map[string]any{"no": true},
		},
	}
	if _, err := c.rci.Post(ctx, payload); err != nil {
		return fmt.Errorf("delete proxy %s: %w", name, err)
	}
	return nil
}

// ProxyUp brings a Proxy interface up.
func (c *ClientImpl) ProxyUp(ctx context.Context, name string) error {
	payload := map[string]any{
		"interface": map[string]any{
			name: map[string]any{"up": true},
		},
	}
	_, err := c.rci.Post(ctx, payload)
	return err
}

// ProxyDown brings a Proxy interface down.
func (c *ClientImpl) ProxyDown(ctx context.Context, name string) error {
	payload := map[string]any{
		"interface": map[string]any{
			name: map[string]any{"down": true},
		},
	}
	_, err := c.rci.Post(ctx, payload)
	return err
}

// ShowProxy returns the current state of a Proxy interface.
// Returns ProxyInfo with Exists=false if the interface does not exist.
// Uses GET /show/interface/<name>; NDMS returns 200 with empty body when
// the interface is absent.
func (c *ClientImpl) ShowProxy(ctx context.Context, name string) (*ProxyInfo, error) {
	raw, err := c.rci.GetRaw(ctx, "/show/interface/"+name)
	if err != nil {
		return nil, fmt.Errorf("show proxy %s: %w", name, err)
	}
	// NDMS returns 200 with empty body when interface does not exist.
	if len(bytes.TrimSpace(raw)) == 0 {
		return &ProxyInfo{Name: name, Exists: false}, nil
	}
	var resp struct {
		ID          string `json:"id"`
		Type        string `json:"type"`
		Description string `json:"description"`
		State       string `json:"state"`
		Link        string `json:"link"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse show proxy %s: %w", name, err)
	}
	// Guard: response JSON without an id means something unexpected.
	if resp.ID == "" {
		return &ProxyInfo{Name: name, Exists: false}, nil
	}
	return &ProxyInfo{
		Name:        name,
		Type:        resp.Type,
		Description: resp.Description,
		State:       resp.State,
		Link:        resp.Link,
		Up:          resp.State == "up",
		Exists:      true,
	}, nil
}

// buildProxyCreatePayload constructs the RCI payload for creating a SOCKS5 proxy interface.
func buildProxyCreatePayload(name, description, upstreamHost string, upstreamPort int, socks5UDP bool) map[string]any {
	proxy := map[string]any{
		"protocol": map[string]any{"proto": "socks5"},
		"upstream": map[string]any{
			"host": upstreamHost,
			"port": strconv.Itoa(upstreamPort),
		},
	}
	if socks5UDP {
		proxy["socks5-udp"] = true
	}
	return map[string]any{
		"interface": map[string]any{
			name: map[string]any{
				"description": description,
				"proxy":       proxy,
				"ip": map[string]any{
					"global": map[string]any{"auto": true},
				},
				"up": true,
			},
		},
	}
}
