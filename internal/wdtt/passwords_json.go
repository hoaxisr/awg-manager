package wdtt

import (
	"encoding/json"
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

// syncPasswordsJSON writes passwords.json for qWDTT monolith wdtt-server.
// ildarmaga ignores this file; safe to write alongside panel.db.
func syncPasswordsJSON(configDir, mainPassword, adminID, botToken string, clients []ServerClient) error {
	dir := strings.TrimSpace(configDir)
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	doc := passwordsJSON{
		MainPassword: strings.TrimSpace(mainPassword),
		AdminID:      strings.TrimSpace(adminID),
		BotToken:     strings.TrimSpace(botToken),
		Passwords:    map[string]passwordsJSONUser{},
		Devices:      map[string]any{},
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
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "passwords.json")
	return os.WriteFile(path, data, 0600)
}
