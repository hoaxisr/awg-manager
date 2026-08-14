package wdtt

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// passwordsJSON mirrors SpaceNeuroX monolith passwords.json (minimal headless subset).
type passwordsJSON struct {
	MainPassword string                       `json:"main_password"`
	AdminID      string                       `json:"admin_id,omitempty"`
	BotToken     string                       `json:"bot_token,omitempty"`
	Passwords    map[string]passwordsJSONUser `json:"passwords"`
	Devices      map[string]any               `json:"devices"`
}

type passwordsJSONUser struct {
	Comment  string `json:"comment,omitempty"`
	VkHash   string `json:"vk_hash,omitempty"`
	DeviceID string `json:"device_id,omitempty"`
}

type passwordsJSONDevice struct {
	IP string `json:"ip"`
}

func passwordsJSONPath(configDir string) string {
	return filepath.Join(strings.TrimSpace(configDir), "passwords.json")
}

func loadPasswordsJSON(configDir string) (passwordsJSON, error) {
	path := passwordsJSONPath(configDir)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return passwordsJSON{}, nil
		}
		return passwordsJSON{}, err
	}
	var doc passwordsJSON
	if err := json.Unmarshal(data, &doc); err != nil {
		return passwordsJSON{}, err
	}
	if doc.Passwords == nil {
		doc.Passwords = map[string]passwordsJSONUser{}
	}
	if doc.Devices == nil {
		doc.Devices = map[string]any{}
	}
	return doc, nil
}

func deviceIPFromPasswordsEntry(v any) string {
	switch d := v.(type) {
	case map[string]any:
		if ip, ok := d["ip"].(string); ok {
			return strings.TrimSpace(ip)
		}
	case passwordsJSONDevice:
		return strings.TrimSpace(d.IP)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		var dev passwordsJSONDevice
		if json.Unmarshal(b, &dev) == nil {
			return strings.TrimSpace(dev.IP)
		}
	}
	return ""
}

// sanitizePasswordsDevices drops devices bound to the server gateway IP (10.66.0.1).
// That address is local on opkgtunN; client traffic from it never forwards/NATs.
func sanitizePasswordsDevices(devices map[string]any) (map[string]any, bool) {
	if len(devices) == 0 {
		return devices, false
	}
	out := make(map[string]any, len(devices))
	changed := false
	for id, entry := range devices {
		if deviceIPFromPasswordsEntry(entry) == DefaultWdttServerGatewayAddr {
			changed = true
			continue
		}
		out[id] = entry
	}
	return out, changed
}

// reserveGatewayIPInDevices marks 10.66.0.1 as used so legacy wdtt-server getNextIP
// skips the OpkgTun gateway before the server binary is rebuilt.
func reserveGatewayIPInDevices(devices map[string]any) map[string]any {
	if devices == nil {
		devices = map[string]any{}
	}
	for _, entry := range devices {
		if deviceIPFromPasswordsEntry(entry) == DefaultWdttServerGatewayAddr {
			return devices
		}
	}
	const reserveID = "__awgm_gateway_reserved__"
	devices[reserveID] = map[string]any{
		"ip":      DefaultWdttServerGatewayAddr,
		"comment": "awg-manager gateway reservation",
	}
	return devices
}

// preparePasswordsJSONForServer merges wdtt.json clients with existing devices and
// removes stale gateway-IP bindings before wdtt-server starts.
func preparePasswordsJSONForServer(configDir, mainPassword, adminID, botToken string, clients []ServerClient) (passwordsJSON, bool, error) {
	existing, err := loadPasswordsJSON(configDir)
	if err != nil {
		return passwordsJSON{}, false, err
	}
	devices, sanitized := sanitizePasswordsDevices(existing.Devices)
	devices = reserveGatewayIPInDevices(devices)
	doc := passwordsJSON{
		MainPassword: strings.TrimSpace(mainPassword),
		AdminID:      strings.TrimSpace(adminID),
		BotToken:     strings.TrimSpace(botToken),
		Passwords:    map[string]passwordsJSONUser{},
		Devices:      devices,
	}
	for _, c := range clients {
		pass := strings.TrimSpace(c.Password)
		if pass == "" || pass == doc.MainPassword {
			continue
		}
		doc.Passwords[pass] = passwordsJSONUser{
			Comment: strings.TrimSpace(c.Comment),
			VkHash:  strings.TrimSpace(c.VkHash),
		}
	}
	return doc, sanitized, nil
}

// syncPasswordsJSON writes passwords.json — the auth source of wdtt-server.
func syncPasswordsJSON(configDir, mainPassword, adminID, botToken string, clients []ServerClient) error {
	dir := strings.TrimSpace(configDir)
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	doc, _, err := preparePasswordsJSONForServer(dir, mainPassword, adminID, botToken, clients)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(passwordsJSONPath(dir), data, 0600)
}
