package ndms

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/rci"
)

// InterfaceInfo holds parsed NDMS "show interface" output.
// The key insight: state: field is unreliable (can show "up" when link is down).
// Use ConfLayer ("running" vs "disabled") to determine NDMS admin intent.
type InterfaceInfo struct {
	State     string // "up", "down", "error"
	Link      string // "up", "down"
	Connected bool
	ConfLayer string // "running", "disabled", "pending"
	Uptime    int    // seconds since interface came up (0 if down)
}

// InterfaceIntent represents what NDMS admin wants for this interface.
// Derived from conf layer, not from state: field.
type InterfaceIntent int

const (
	// IntentDown means NDMS has disabled this interface (conf: disabled).
	// This is the zero value — safe default when ShowInterface fails.
	IntentDown InterfaceIntent = iota
	// IntentUp means NDMS wants this interface running (conf: running).
	IntentUp
)

// Intent returns the NDMS admin intent derived from ConfLayer.
// conf: running → IntentUp (admin wants it up, including after reboot).
// conf: disabled → IntentDown (admin explicitly turned it off).
func (info InterfaceInfo) Intent() InterfaceIntent {
	if info.ConfLayer == "running" {
		return IntentUp
	}
	return IntentDown
}

// LinkUp returns true if the link layer is up.
func (info InterfaceInfo) LinkUp() bool {
	return info.Link == "up"
}

// ParseInterfaceInfo parses interface output from NDMS.
// Supports both JSON format (from RCI) and text format (legacy ndmc -c).
// Extracts top-level fields (state, link, connected) and conf layer from summary section.
func ParseInterfaceInfo(output string) (InterfaceInfo, error) {
	output = strings.TrimSpace(output)
	if output == "" {
		return InterfaceInfo{}, fmt.Errorf("empty input")
	}

	// JSON format (from RCI)
	if output[0] == '{' {
		return parseInterfaceInfoJSON(output)
	}

	// Text format (legacy ndmc -c)
	if strings.Contains(output, "not found") {
		return InterfaceInfo{}, fmt.Errorf("interface not found")
	}

	var info InterfaceInfo
	inSummary := false
	found := false

	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)

		if trimmed == "summary:" {
			inSummary = true
			continue
		}

		if !inSummary {
			// Top-level fields
			if v, ok := parseField(trimmed, "state"); ok {
				info.State = v
				found = true
			} else if v, ok := parseField(trimmed, "link"); ok {
				info.Link = v
			} else if v, ok := parseField(trimmed, "connected"); ok {
				info.Connected = v == "yes"
			} else if v, ok := parseField(trimmed, "uptime"); ok {
				fmt.Sscanf(v, "%d", &info.Uptime)
			}
		} else {
			// Summary > layer fields
			if v, ok := parseField(trimmed, "conf"); ok {
				info.ConfLayer = v
			}
		}
	}

	if !found {
		return InterfaceInfo{}, fmt.Errorf("no state field found in output")
	}

	return info, nil
}

// parseInterfaceInfoJSON parses RCI JSON format into InterfaceInfo.
func parseInterfaceInfoJSON(data string) (InterfaceInfo, error) {
	var info rci.InterfaceInfo
	if err := json.Unmarshal([]byte(data), &info); err != nil {
		return InterfaceInfo{}, fmt.Errorf("parse JSON: %w", err)
	}
	if info.State == "" {
		return InterfaceInfo{}, fmt.Errorf("no state field found in JSON")
	}
	return InterfaceInfo{
		State:     info.State,
		Link:      info.Link,
		Connected: info.Connected == "yes",
		ConfLayer: info.Summary.Layer.Conf,
		Uptime:    info.Uptime,
	}, nil
}

// parseField extracts value from "key: value" line.
func parseField(line, key string) (string, bool) {
	prefix := key + ": "
	if strings.HasPrefix(line, prefix) {
		return strings.TrimSpace(line[len(prefix):]), true
	}
	return "", false
}
