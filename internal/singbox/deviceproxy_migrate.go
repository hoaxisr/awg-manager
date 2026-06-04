// internal/singbox/deviceproxy_migrate.go
package singbox

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// MigrateDeviceProxyOutOfTunnels checks if 10-tunnels.json contains
// device-proxy artefacts (legacy single-file layout where device-proxy
// injected itself into the tunnels file). If found, splits them into
// a fresh 30-deviceproxy.json and rewrites 10-tunnels.json without
// the device-proxy bits.
//
// Idempotent — if 10-tunnels.json has no device-proxy artefacts, or
// 30-deviceproxy.json already exists, this is a no-op. Migration only
// looks at the active layout: it does NOT recurse into disabled/.
//
// configDir is the sing-box config.d directory (typically
// /opt/etc/sing-box/config.d).
func MigrateDeviceProxyOutOfTunnels(configDir string) error {
	tunnelsPath := filepath.Join(configDir, "10-tunnels.json")
	deviceProxyPath := filepath.Join(configDir, "30-deviceproxy.json")

	// Already split? Nothing to do.
	if _, err := os.Stat(deviceProxyPath); err == nil {
		return nil
	}

	data, err := os.ReadFile(tunnelsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read tunnels: %w", err)
	}

	cfg := NewConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("parse tunnels: %w", err)
	}

	if !cfg.HasDeviceProxy() {
		return nil
	}

	// Build the device-proxy slot by extracting it from the loaded cfg.
	extracted := cfg.ExtractDeviceProxy()

	extractedJSON, err := json.MarshalIndent(extracted, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal extracted: %w", err)
	}
	if err := writeJSONAtomic(deviceProxyPath, extractedJSON); err != nil {
		return fmt.Errorf("write deviceproxy: %w", err)
	}

	// Persist tunnels stripped of device-proxy.
	cfg.RemoveDeviceProxy()
	strippedJSON, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal tunnels: %w", err)
	}
	if err := writeJSONAtomic(tunnelsPath, strippedJSON); err != nil {
		return fmt.Errorf("write tunnels: %w", err)
	}

	return nil
}

// writeJSONAtomic writes data to path atomically (unique temp + rename) via
// storage.AtomicWrite, so a crash or concurrent writer never leaves a torn or
// collided file behind.
func writeJSONAtomic(path string, data []byte) error {
	return storage.AtomicWrite(path, data)
}
