package ndms

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const rciBaseURL = "http://localhost:79/rci"

// rciGet fetches a RCI endpoint and decodes JSON into dst.
// Returns error on HTTP failures or non-200 status.
func rciGet(ctx context.Context, client *http.Client, path string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", rciBaseURL+path, nil)
	if err != nil {
		return fmt.Errorf("rci %s: %w", path, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("rci %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("rci %s: status %d", path, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("rci %s: read body: %w", path, err)
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("rci %s: decode: %w", path, err)
	}
	return nil
}

// rciInterfaceInfo represents a single interface from /rci/show/interface/<name>.
type rciInterfaceInfo struct {
	State         string `json:"state"`
	Link          string `json:"link"`
	Connected     string `json:"connected"`
	InterfaceName string `json:"interface-name"`
	Type          string `json:"type"`
	Description   string `json:"description"`
	Address       string `json:"address"`
	Mask          string `json:"mask"`
	SecurityLevel string `json:"security-level"`
	Priority      int    `json:"priority"`
	Summary       struct {
		Layer struct {
			Conf string `json:"conf"`
			Link string `json:"link"`
			IPv4 string `json:"ipv4"`
			IPv6 string `json:"ipv6"`
		} `json:"layer"`
	} `json:"summary"`
}

// rciRouteEntry represents an element of /rci/show/ip/route array.
type rciRouteEntry struct {
	Destination string `json:"destination"`
	Gateway     string `json:"gateway"`
	Interface   string `json:"interface"`
}

// rciIPv6RouteEntry represents an element of /rci/show/ipv6/route array.
type rciIPv6RouteEntry struct {
	Destination string `json:"destination"`
	Gateway     string `json:"gateway"`
	Interface   string `json:"interface"`
}

// rciDHCPClient represents an element of /rci/show/ip/dhcp/client array.
type rciDHCPClient struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	State string `json:"state"`
}

// rciHotspotResponse wraps the /rci/show/ip/hotspot response.
// The RCI endpoint returns {"host": [...]}, not a flat array.
type rciHotspotResponse struct {
	Host []rciHotspotHost `json:"host"`
}

// rciHotspotHost represents a single host entry in the hotspot response.
type rciHotspotHost struct {
	IP       string `json:"ip"`
	MAC      string `json:"mac"`
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
	Active   any    `json:"active"` // bool or string depending on firmware
}
