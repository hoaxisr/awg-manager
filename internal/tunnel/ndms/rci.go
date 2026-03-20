package ndms

import (
	"bytes"
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

// rciPost sends a JSON payload to RCI via HTTP POST and returns the response body.
func rciPost(ctx context.Context, client *http.Client, path string, payload interface{}) (json.RawMessage, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		return nil, fmt.Errorf("rci POST %s: marshal: %w", path, err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", rciBaseURL+path, &buf)
	if err != nil {
		return nil, fmt.Errorf("rci POST %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rci POST %s: %w", path, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("rci POST %s: read body: %w", path, err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rci POST %s: status %d: %s", path, resp.StatusCode, string(data))
	}

	// NDMS RCI returns HTTP 200 even on errors — check response body for error status.
	if errMsg := extractRCIError(data); errMsg != "" {
		return json.RawMessage(data), fmt.Errorf("rci POST %s: %s", path, errMsg)
	}

	return json.RawMessage(data), nil
}

// extractRCIError recursively searches RCI JSON response for {"status":"error","message":"..."}.
// Returns the error message if found, empty string otherwise.
func extractRCIError(data []byte) string {
	var raw interface{}
	if json.Unmarshal(data, &raw) != nil {
		return ""
	}
	return findRCIError(raw)
}

func findRCIError(v interface{}) string {
	switch val := v.(type) {
	case map[string]interface{}:
		// Check if this object is a status error entry
		if s, ok := val["status"]; ok {
			if str, ok := s.(string); ok && str == "error" {
				if msg, ok := val["message"].(string); ok {
					return msg
				}
				return "unknown RCI error"
			}
		}
		// Check "status" array (NDMS wraps errors in {"status": [...]})
		if arr, ok := val["status"].([]interface{}); ok {
			for _, item := range arr {
				if msg := findRCIError(item); msg != "" {
					return msg
				}
			}
		}
		// Recurse into all values
		for _, child := range val {
			if msg := findRCIError(child); msg != "" {
				return msg
			}
		}
	case []interface{}:
		for _, item := range val {
			if msg := findRCIError(item); msg != "" {
				return msg
			}
		}
	}
	return ""
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

// rciPingCheckListResponse represents /show/ping-check/ response.
// Format: {"pingcheck": [{"profile": "name", "host": ["8.8.8.8"], ...}, ...]}
type rciPingCheckListResponse struct {
	PingCheck []rciPingCheckProfile `json:"pingcheck"`
}

// rciPingCheckProfile represents a single profile in the ping-check list.
type rciPingCheckProfile struct {
	Profile        string                             `json:"profile"`
	Host           []string                           `json:"host"`
	Mode           string                             `json:"mode"`
	UpdateInterval int                                `json:"update-interval"`
	MaxFails       int                                `json:"max-fails"`
	MinSuccess     int                                `json:"min-success"`
	Timeout        int                                `json:"timeout"`
	Port           int                                `json:"port"`
	Interface      map[string]rciPingCheckIfaceStatus `json:"interface"`
}

// rciPingCheckIfaceStatus represents a bound interface's check status.
type rciPingCheckIfaceStatus struct {
	SuccessCount int    `json:"successcount"`
	FailCount    int    `json:"failcount"`
	Status       string `json:"status"` // "pass", "fail", ""
}
