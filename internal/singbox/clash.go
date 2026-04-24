package singbox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// ClashClient is a thin HTTP client for sing-box's Clash API.
type ClashClient struct {
	address string // e.g. "127.0.0.1:9099" — see singbox.clashAPIAddr
	http    *http.Client
}

func NewClashClient(address string) *ClashClient {
	return &ClashClient{
		address: address,
		http:    &http.Client{Timeout: 5 * time.Second},
	}
}

// ClashProxy mirrors Clash API /proxies item shape.
type ClashProxy struct {
	Name    string         `json:"name"`
	Type    string         `json:"type"`
	Now     string         `json:"now,omitempty"`
	All     []string       `json:"all,omitempty"`
	UDP     bool           `json:"udp,omitempty"`
	History []DelayHistory `json:"history,omitempty"`
}

type DelayHistory struct {
	Time  string `json:"time"`
	Delay int    `json:"delay"`
}

// GetProxies returns the map of proxies keyed by name.
func (c *ClashClient) GetProxies() (map[string]ClashProxy, error) {
	u := fmt.Sprintf("http://%s/proxies", c.address)
	resp, err := c.http.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("proxies status: %d", resp.StatusCode)
	}
	var wrap struct {
		Proxies map[string]ClashProxy `json:"proxies"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&wrap); err != nil {
		return nil, err
	}
	return wrap.Proxies, nil
}

// TestDelay triggers a latency test for a proxy via Clash API.
func (c *ClashClient) TestDelay(name, testURL string, timeout time.Duration) (int, error) {
	q := url.Values{}
	q.Set("url", testURL)
	q.Set("timeout", fmt.Sprintf("%d", timeout.Milliseconds()))
	u := fmt.Sprintf("http://%s/proxies/%s/delay?%s", c.address, url.PathEscape(name), q.Encode())
	resp, err := c.http.Get(u)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("delay status: %d", resp.StatusCode)
	}
	var r struct {
		Delay int `json:"delay"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return 0, err
	}
	return r.Delay, nil
}

// IsHealthy checks Clash API availability (fast health probe).
func (c *ClashClient) IsHealthy() bool {
	cli := &http.Client{Timeout: 1 * time.Second}
	resp, err := cli.Get(fmt.Sprintf("http://%s/version", c.address))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

// Address returns the Clash API address for WebSocket proxying.
func (c *ClashClient) Address() string { return c.address }

// SetSelector tells a running sing-box via the Clash API to switch the
// active member of a selector outbound. Live switch: existing
// connections pinned to previous members stay alive; new connections
// go through memberTag.
func (c *ClashClient) SetSelector(selectorTag, memberTag string) error {
	body, err := json.Marshal(map[string]string{"name": memberTag})
	if err != nil {
		return err
	}
	u := fmt.Sprintf("http://%s/proxies/%s", c.address, url.PathEscape(selectorTag))
	req, err := http.NewRequest(http.MethodPut, u, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("clash SetSelector %s->%s: HTTP %d", selectorTag, memberTag, resp.StatusCode)
	}
	return nil
}

// SelectorActive returns the currently-active member (`now`) of a
// named selector outbound. Queries Clash GET /proxies/<tag>. Returns
// ("", nil) when the selector is reported absent by the daemon so
// callers can treat "no selector yet" as distinct from a transport
// error.
func (c *ClashClient) SelectorActive(selectorTag string) (string, error) {
	u := fmt.Sprintf("http://%s/proxies/%s", c.address, url.PathEscape(selectorTag))
	resp, err := c.http.Get(u)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("clash SelectorActive %s: HTTP %d", selectorTag, resp.StatusCode)
	}
	var body struct {
		Now string `json:"now"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("decode clash response: %w", err)
	}
	return body.Now, nil
}
