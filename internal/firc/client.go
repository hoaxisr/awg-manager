package firc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// APIError represents a structured HTTP error response returned by fircd.
type APIError struct {
	StatusCode int
	Message    string `json:"error"`
	Field      string `json:"field,omitempty"`
	Tunnel     string `json:"tunnel,omitempty"`
	Group      string `json:"group,omitempty"`
}

func (e *APIError) Error() string {
	var parts []string
	if e.Tunnel != "" {
		parts = append(parts, fmt.Sprintf("tunnel=%s", e.Tunnel))
	}
	if e.Group != "" {
		parts = append(parts, fmt.Sprintf("group=%s", e.Group))
	}
	if e.Field != "" {
		parts = append(parts, fmt.Sprintf("field=%s", e.Field))
	}
	msg := e.Message
	if msg == "" {
		msg = fmt.Sprintf("HTTP %d", e.StatusCode)
	}
	if len(parts) > 0 {
		return fmt.Sprintf("fircd (%s): %s", strings.Join(parts, ", "), msg)
	}
	return fmt.Sprintf("fircd: %s", msg)
}

// Client communicates with fircd over its Unix domain socket (/tmp/firc/firc.sock)
// or an HTTP endpoint.
type Client struct {
	socketPath string
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a Client bound to the fircd Unix domain socket.
func NewClient(socketPath string) *Client {
	if strings.TrimSpace(socketPath) == "" {
		socketPath = DefaultSocketPath
	}
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
		MaxIdleConns:          8,
		IdleConnTimeout:       30 * time.Second,
		DisableCompression:    true,
		MaxConnsPerHost:       16,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	return &Client{
		socketPath: socketPath,
		baseURL:    "http://unix",
		httpClient: &http.Client{
			Transport: tr,
			Timeout:   35 * time.Second,
		},
	}
}

// NewHTTPClient creates a Client bound to an explicit HTTP baseURL (useful for unit tests).
func NewHTTPClient(baseURL string, hc *http.Client) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 35 * time.Second}
	}
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: hc,
	}
}

// SocketPath returns the configured Unix socket path (empty for TCP HTTP clients).
func (c *Client) SocketPath() string {
	return c.socketPath
}

func (c *Client) doJSON(ctx context.Context, method, path string, reqBody, respBody interface{}) error {
	var bodyReader io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return err
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		apiErr := &APIError{StatusCode: resp.StatusCode}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, apiErr)
			if apiErr.Message == "" {
				apiErr.Message = strings.TrimSpace(string(raw))
			}
		}
		return apiErr
	}

	if respBody != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, respBody); err != nil {
			return fmt.Errorf("decode response from %s %s: %w", method, path, err)
		}
	}
	return nil
}

// Ping checks whether fircd is reachable and responsive via GET /api/v1/system/netfilter.
func (c *Client) Ping(ctx context.Context) (*NetfilterHealth, error) {
	var h NetfilterHealth
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/system/netfilter", nil, &h); err != nil {
		return nil, err
	}
	return &h, nil
}

// GetSettings retrieves live and saved firc.conf settings via GET /api/v1/system/settings.
func (c *Client) GetSettings(ctx context.Context) (*SettingsResponse, error) {
	var out SettingsResponse
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/system/settings", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PutSettings updates firc.conf settings via PUT /api/v1/system/settings.
func (c *Client) PutSettings(ctx context.Context, changes map[string]interface{}) ([]string, error) {
	payload := map[string]interface{}{"settings": changes}
	var out struct {
		Applied        []string `json:"applied"`
		PendingRestart []string `json:"pendingRestart"`
	}
	if err := c.doJSON(ctx, http.MethodPut, "/api/v1/system/settings", payload, &out); err != nil {
		return nil, err
	}
	return out.PendingRestart, nil
}

// GetInterfaces lists router interfaces known to fircd via GET /api/v1/system/interfaces.
func (c *Client) GetInterfaces(ctx context.Context) ([]SystemInterface, error) {
	var out struct {
		Interfaces []SystemInterface `json:"interfaces"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/system/interfaces", nil, &out); err != nil {
		return nil, err
	}
	return out.Interfaces, nil
}

// GetEvents reads the fircd event ring (logs, DNS decisions, bypasses) since the given sequence cursor.
func (c *Client) GetEvents(ctx context.Context, since uint64) (*EventsResponse, error) {
	var out EventsResponse
	path := fmt.Sprintf("/api/v1/system/events?since=%d", since)
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetAllEvents drains all available pages of the fircd event ring starting from since,
// ensuring callers reach the newest real-time events even when the ring buffer exceeds 1024 entries.
func (c *Client) GetAllEvents(ctx context.Context, since uint64) (*EventsResponse, error) {
	var combined EventsResponse
	cur := since
	for page := 0; page < 32; page++ {
		res, err := c.GetEvents(ctx, cur)
		if err != nil {
			if page == 0 {
				return nil, err
			}
			break
		}
		if page == 0 {
			combined.Boot = res.Boot
			combined.Dropped = res.Dropped
		} else {
			combined.Dropped += res.Dropped
		}
		combined.Events = append(combined.Events, res.Events...)
		if len(combined.Events) > 4096 {
			combined.Events = combined.Events[len(combined.Events)-4096:]
		}
		combined.Next = res.Next
		if len(res.Events) < 1024 || res.Next <= cur {
			break
		}
		cur = res.Next
	}
	return &combined, nil
}

// GetDNSStats reads aggregated 24-hour DNS statistics from GET /api/v1/system/dnsstats.
func (c *Client) GetDNSStats(ctx context.Context, limit int) (*DNSStatsResponse, error) {
	if limit <= 0 {
		limit = 500
	}
	var out DNSStatsResponse
	path := fmt.Sprintf("/api/v1/system/dnsstats?limit=%d", limit)
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetAutolist reads the FIRC bypass traffic diagnostics state from GET /api/v1/system/autolist.
func (c *Client) GetAutolist(ctx context.Context) (*AutolistStatus, error) {
	var out AutolistStatus
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/system/autolist", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RecheckAutolist triggers an immediate recheck of bypass traffic rules via POST /api/v1/system/autolist/check.
func (c *Client) RecheckAutolist(ctx context.Context) error {
	return c.doJSON(ctx, http.MethodPost, "/api/v1/system/autolist/check", nil, nil)
}

// ProbeAutolistHost queues a manual host reachability probe via POST /api/v1/system/autolist/probe.
func (c *Client) ProbeAutolistHost(ctx context.Context, host string) (*AutolistProbes, error) {
	payload := map[string]string{"host": strings.TrimSpace(host)}
	var raw struct {
		AutolistProbes
		Probes *AutolistProbes `json:"probes"`
	}
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/system/autolist/probe", payload, &raw); err != nil {
		return nil, err
	}
	if raw.Probes != nil {
		return raw.Probes, nil
	}
	return &raw.AutolistProbes, nil
}

// AddAutolistCandidate confirms and adds a bypass candidate domain to the routing group via POST /api/v1/system/autolist/candidates/{domain}/add.
func (c *Client) AddAutolistCandidate(ctx context.Context, domain string) error {
	path := fmt.Sprintf("/api/v1/system/autolist/candidates/%s/add", url.PathEscape(strings.TrimSpace(domain)))
	return c.doJSON(ctx, http.MethodPost, path, nil, nil)
}

// DismissAutolistCandidate dismisses a bypass candidate domain via POST /api/v1/system/autolist/candidates/{domain}/dismiss.
func (c *Client) DismissAutolistCandidate(ctx context.Context, domain string) error {
	path := fmt.Sprintf("/api/v1/system/autolist/candidates/%s/dismiss", url.PathEscape(strings.TrimSpace(domain)))
	return c.doJSON(ctx, http.MethodPost, path, nil, nil)
}

// DismissAutolistAddress dismisses a bypass address entry via POST /api/v1/system/autolist/addresses/{address}/dismiss.
func (c *Client) DismissAutolistAddress(ctx context.Context, address, proto string, port uint16) error {
	path := fmt.Sprintf("/api/v1/system/autolist/addresses/%s/dismiss", url.PathEscape(strings.TrimSpace(address)))
	payload := map[string]interface{}{
		"proto": strings.ToLower(strings.TrimSpace(proto)),
		"port":  port,
	}
	return c.doJSON(ctx, http.MethodPost, path, payload, nil)
}

// GetTunnels returns the configured firc tunnels from GET /api/v1/tunnels.
func (c *Client) GetTunnels(ctx context.Context) ([]Tunnel, error) {
	var out struct {
		Tunnels []Tunnel `json:"tunnels"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/tunnels", nil, &out); err != nil {
		return nil, err
	}
	return out.Tunnels, nil
}

// PutTunnels replaces the configured firc tunnels via PUT /api/v1/tunnels.
func (c *Client) PutTunnels(ctx context.Context, tunnels []Tunnel) (*PutTunnelsResponse, error) {
	if tunnels == nil {
		tunnels = []Tunnel{}
	}
	payload := map[string]interface{}{"tunnels": tunnels}
	var out PutTunnelsResponse
	if err := c.doJSON(ctx, http.MethodPut, "/api/v1/tunnels", payload, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetTunnelsState returns live runtime telemetry for all tunnels via GET /api/v1/tunnels/state.
func (c *Client) GetTunnelsState(ctx context.Context) ([]TunnelState, error) {
	var out struct {
		Tunnels []TunnelState `json:"tunnels"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/tunnels/state", nil, &out); err != nil {
		return nil, err
	}
	return out.Tunnels, nil
}

// PreviewTunnel previews parsed nodes and filter matching for a draft tunnel via POST /api/v1/tunnels/preview.
func (c *Client) PreviewTunnel(ctx context.Context, draft Tunnel) (*PreviewResponse, error) {
	payload := map[string]interface{}{"tunnel": draft}
	var out PreviewResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/tunnels/preview", payload, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ProbeTunnel triggers a live health/latency probe of all nodes in a saved tunnel (by ID) or draft tunnel.
func (c *Client) ProbeTunnel(ctx context.Context, id string, draft *Tunnel) (*ProbeResponse, error) {
	payload := make(map[string]interface{})
	if id != "" {
		payload["id"] = id
	} else if draft != nil {
		payload["tunnel"] = draft
	}
	var out ProbeResponse
	if err := c.doJSON(ctx, http.MethodPost, "/api/v1/tunnels/probe", payload, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RefreshTunnel triggers an immediate subscription fetch for a tunnel via POST /api/v1/tunnels/{id}/refresh.
func (c *Client) RefreshTunnel(ctx context.Context, id string) (bool, error) {
	var out struct {
		Queued bool `json:"queued"`
	}
	path := fmt.Sprintf("/api/v1/tunnels/%s/refresh", url.PathEscape(id))
	if err := c.doJSON(ctx, http.MethodPost, path, nil, &out); err != nil {
		return false, err
	}
	return out.Queued, nil
}

// RestartTunnel restarts a running tunvless tunnel process via POST /api/v1/tunnels/{id}/restart.
func (c *Client) RestartTunnel(ctx context.Context, id string) (bool, error) {
	var out struct {
		Queued bool `json:"queued"`
	}
	path := fmt.Sprintf("/api/v1/tunnels/%s/restart", url.PathEscape(id))
	if err := c.doJSON(ctx, http.MethodPost, path, nil, &out); err != nil {
		return false, err
	}
	return out.Queued, nil
}

// SelectNode pins or prioritizes a specific nodeKey at the front of the tunnel's Order list
// and applies the updated tunnel configuration via PUT /api/v1/tunnels.
func (c *Client) SelectNode(ctx context.Context, tunnelID, nodeKey string) (*PutTunnelsResponse, error) {
	tunnels, err := c.GetTunnels(ctx)
	if err != nil {
		return nil, err
	}
	found := false
	for i := range tunnels {
		if tunnels[i].ID != tunnelID && tunnels[i].Device != tunnelID {
			continue
		}
		found = true
		newOrder := make([]string, 0, len(tunnels[i].Order)+1)
		newOrder = append(newOrder, nodeKey)
		for _, k := range tunnels[i].Order {
			if k != nodeKey {
				newOrder = append(newOrder, k)
			}
		}
		tunnels[i].Order = newOrder
		if tunnels[i].By == "" {
			tunnels[i].By = "order"
		}
		break
	}
	if !found {
		return nil, &APIError{StatusCode: http.StatusNotFound, Message: "no tunnel with this id", Tunnel: tunnelID}
	}
	return c.PutTunnels(ctx, tunnels)
}

// GetGroups retrieves all routing groups from GET /api/v1/groups.
func (c *Client) GetGroups(ctx context.Context, withRules bool) ([]Group, error) {
	path := "/api/v1/groups"
	if withRules {
		path += "?with_rules=true"
	}
	var out struct {
		Groups []Group `json:"groups"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Groups, nil
}

// PutGroups atomically replaces all routing groups via PUT /api/v1/groups.
func (c *Client) PutGroups(ctx context.Context, groups []Group, save bool) ([]Group, error) {
	if groups == nil {
		groups = []Group{}
	}
	path := "/api/v1/groups"
	if save {
		path += "?save=true"
	}
	payload := map[string]interface{}{"groups": groups}
	var out struct {
		Groups []Group `json:"groups"`
	}
	if err := c.doJSON(ctx, http.MethodPut, path, payload, &out); err != nil {
		return nil, err
	}
	if len(out.Groups) == 0 && len(groups) > 0 {
		return groups, nil
	}
	return out.Groups, nil
}

// CreateGroup creates a single routing group via POST /api/v1/groups.
func (c *Client) CreateGroup(ctx context.Context, g Group, save bool) (*Group, error) {
	path := "/api/v1/groups"
	if save {
		path += "?save=true"
	}
	var out Group
	if err := c.doJSON(ctx, http.MethodPost, path, g, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateGroup updates a single routing group via PUT /api/v1/groups/{groupID}.
func (c *Client) UpdateGroup(ctx context.Context, groupID string, g Group, save bool) (*Group, error) {
	path := fmt.Sprintf("/api/v1/groups/%s", url.PathEscape(groupID))
	if save {
		path += "?save=true"
	}
	var out Group
	if err := c.doJSON(ctx, http.MethodPut, path, g, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteGroup removes a routing group via DELETE /api/v1/groups/{groupID}.
func (c *Client) DeleteGroup(ctx context.Context, groupID string, save bool) error {
	path := fmt.Sprintf("/api/v1/groups/%s", url.PathEscape(groupID))
	if save {
		path += "?save=true"
	}
	return c.doJSON(ctx, http.MethodDelete, path, nil, nil)
}

// GetGroupListRules retrieves rules from an external list attached to a routing group.
func (c *Client) GetGroupListRules(ctx context.Context, groupID string, offset, limit int) ([]Rule, error) {
	if limit <= 0 {
		limit = 5000
	}
	path := fmt.Sprintf("/api/v1/groups/%s/list/rules?offset=%d&limit=%d", url.PathEscape(groupID), offset, limit)
	var out struct {
		Rules []Rule `json:"rules"`
	}
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Rules, nil
}

// NotifyNetfilterHook forwards a Keenetic netfilter.d event to fircd.
func (c *Client) NotifyNetfilterHook(ctx context.Context, hookType, table string) error {
	payload := map[string]string{
		"type":  hookType,
		"table": table,
	}
	return c.doJSON(ctx, http.MethodPost, "/api/v1/system/hooks/netfilterd", payload, nil)
}

// NotifyIflayerHook forwards a Keenetic iflayerchanged.d event to fircd.
func (c *Client) NotifyIflayerHook(ctx context.Context, iface, layer, level string) error {
	payload := map[string]string{
		"iface": iface,
		"layer": layer,
		"level": level,
	}
	return c.doJSON(ctx, http.MethodPost, "/api/v1/system/hooks/iflayer", payload, nil)
}
