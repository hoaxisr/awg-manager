package pingcheck

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// SingboxCheckConfig holds monitoring settings for a singbox tunnel.
type SingboxCheckConfig struct {
	Enabled       bool `json:"enabled"`
	Interval      int  `json:"intervalSec"` // seconds
	FailThreshold int  `json:"failThreshold"`
}

// loadSingboxConfigs reads the per-tunnel pingcheck config for singbox.
// Returns an empty map if the file does not exist.
func loadSingboxConfigs(dir string) (map[string]*SingboxCheckConfig, error) {
	path := filepath.Join(dir, "pingcheck.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]*SingboxCheckConfig), nil
		}
		return nil, fmt.Errorf("read singbox pingcheck config: %w", err)
	}
	var cfgs map[string]*SingboxCheckConfig
	if err := json.Unmarshal(data, &cfgs); err != nil {
		return nil, fmt.Errorf("parse singbox pingcheck config: %w", err)
	}
	if cfgs == nil {
		cfgs = make(map[string]*SingboxCheckConfig)
	}
	return cfgs, nil
}

// saveSingboxConfigs writes the configs atomically (tmp file + rename).
func saveSingboxConfigs(dir string, cfgs map[string]*SingboxCheckConfig) error {
	path := filepath.Join(dir, "pingcheck.json")
	data, err := json.MarshalIndent(cfgs, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp) // best-effort cleanup
		return err
	}
	return nil
}
