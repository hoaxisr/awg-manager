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
// Идемпотентность считается по СОДЕРЖИМОМУ 10-tunnels.json, а не по
// существованию 30-deviceproxy.json: провал ВТОРОЙ записи (перезапись
// 10-tunnels) оставлял device-proxy в обоих слотах, и stat-гард запирал
// это состояние навсегда — дубль `device-proxy-in` валит preflight
// холодного старта. Существующий 30-файл при доборе НЕ перезаписывается:
// с момента раскола им владеет Reconcile device-proxy-сервиса (F73).
//
// Migration only looks at the active layout: it does NOT recurse into
// disabled/.
//
// configDir is the sing-box config.d directory (typically
// /opt/etc/sing-box/config.d).
//
// Возвращает признак «файлы переписаны» — по нему демон решает, надо ли
// перечитать конфиг пережившего рестарт sing-box (F34): без него миграция
// доезжала до живого процесса только со случайным reload по другому поводу.
func MigrateDeviceProxyOutOfTunnels(configDir string) (bool, error) {
	tunnelsPath := filepath.Join(configDir, "10-tunnels.json")
	deviceProxyPath := filepath.Join(configDir, "30-deviceproxy.json")

	data, err := os.ReadFile(tunnelsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read tunnels: %w", err)
	}

	cfg := NewConfig()
	if err := json.Unmarshal(data, cfg); err != nil {
		return false, fmt.Errorf("parse tunnels: %w", err)
	}

	// Покрывает и «уже мигрировано»: чистый 10-tunnels — штатный no-op.
	if !cfg.HasDeviceProxy() {
		return false, nil
	}

	// 10-tunnels ещё несёт device-proxy → раскол не доехал. Существование
	// 30-файла этого не опровергает, но и переписывать его нельзя.
	if _, err := os.Stat(deviceProxyPath); err != nil {
		extracted := cfg.ExtractDeviceProxy()

		extractedJSON, err := json.MarshalIndent(extracted, "", "  ")
		if err != nil {
			return false, fmt.Errorf("marshal extracted: %w", err)
		}
		if err := writeJSONAtomic(deviceProxyPath, extractedJSON); err != nil {
			return false, fmt.Errorf("write deviceproxy: %w", err)
		}
	}

	// Persist tunnels stripped of device-proxy.
	cfg.RemoveDeviceProxy()
	strippedJSON, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshal tunnels: %w", err)
	}
	if err := writeJSONAtomic(tunnelsPath, strippedJSON); err != nil {
		return false, fmt.Errorf("write tunnels: %w", err)
	}

	return true, nil
}

// writeJSONAtomic writes data to path atomically (unique temp + rename) via
// storage.AtomicWrite, so a crash or concurrent writer never leaves a torn or
// collided file behind.
func writeJSONAtomic(path string, data []byte) error {
	return storage.AtomicWrite(path, data)
}
