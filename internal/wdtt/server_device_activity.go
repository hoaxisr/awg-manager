package wdtt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	rawSessionRegisteredRE = regexp.MustCompile(`\[RAW\] Сессия (\S+) зарегистрирована`)
	rawSessionEndedRE      = regexp.MustCompile(`\[RAW\] Сессия (\S+) \(ip=`)
	rawRelayDeviceRE       = regexp.MustCompile(`device=(\S+)\)`)
	rawDeviceAssignRE      = regexp.MustCompile(`\[RAW\] Устройство (\S+) →`)
	wgNewDeviceRE          = regexp.MustCompile(`\[WG\] Новое устройство (\S+)`)
)

type serverStatsSnapshot struct {
	Active        int      `json:"active"`
	Devices       int      `json:"devices"`
	Timestamp     int64    `json:"timestamp"`
	ActiveIDs     []string `json:"active_ids"`
	ActiveIDsAlt  []string `json:"activeIds"`
	ActiveDevices []string `json:"active_devices"`
}

func serverStatsLogPath(instanceID string) string {
	id := strings.TrimSpace(instanceID)
	if id == "" {
		id = DefaultInstanceID
	}
	return filepath.Join("/tmp", "awg-wdtt-server-"+id+".log")
}

func readServerStatsSnapshot(instanceID string) (serverStatsSnapshot, bool) {
	data, err := os.ReadFile(serverStatsLogPath(instanceID))
	if err != nil || len(data) == 0 {
		return serverStatsSnapshot{}, false
	}
	var snap serverStatsSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return serverStatsSnapshot{}, false
	}
	return snap, true
}

func activeIDsFromStatsSnap(snap serverStatsSnapshot) map[string]bool {
	out := map[string]bool{}
	for _, id := range snap.ActiveIDs {
		if id = strings.TrimSpace(id); id != "" {
			out[id] = true
		}
	}
	for _, id := range snap.ActiveIDsAlt {
		if id = strings.TrimSpace(id); id != "" {
			out[id] = true
		}
	}
	for _, id := range snap.ActiveDevices {
		if id = strings.TrimSpace(id); id != "" {
			out[id] = true
		}
	}
	return out
}

func mergeActiveMaps(into map[string]bool, from map[string]bool) {
	for id, on := range from {
		if on {
			into[id] = true
		}
	}
}

func parseRawActiveDevicesFromServerLog(log string) map[string]bool {
	active := map[string]bool{}
	if strings.TrimSpace(log) == "" {
		return active
	}
	for _, line := range strings.Split(log, "\n") {
		if m := rawSessionEndedRE.FindStringSubmatch(line); len(m) > 1 {
			delete(active, m[1])
			continue
		}
		if m := rawSessionRegisteredRE.FindStringSubmatch(line); len(m) > 1 {
			active[m[1]] = true
			continue
		}
		if m := rawRelayDeviceRE.FindStringSubmatch(line); len(m) > 1 {
			active[m[1]] = true
			continue
		}
		if m := rawDeviceAssignRE.FindStringSubmatch(line); len(m) > 1 {
			active[m[1]] = true
		}
	}
	return active
}

func parseWGActiveDevicesFromServerLog(log string) map[string]bool {
	active := map[string]bool{}
	if strings.TrimSpace(log) == "" {
		return active
	}
	for _, line := range strings.Split(log, "\n") {
		if m := wgNewDeviceRE.FindStringSubmatch(line); len(m) > 1 {
			active[m[1]] = true
		}
	}
	return active
}

// parseActiveDevicesFromServerLog — union RAW+WG (legacy/tests).
func parseActiveDevicesFromServerLog(log string) map[string]bool {
	out := parseRawActiveDevicesFromServerLog(log)
	mergeActiveMaps(out, parseWGActiveDevicesFromServerLog(log))
	return out
}

func filterActiveIDsForMode(activeIDs map[string]bool, doc passwordsJSON, mode string) map[string]bool {
	out := map[string]bool{}
	for id := range activeIDs {
		entry, ok := doc.Devices[id]
		if !ok {
			continue
		}
		fields := parsePasswordsDeviceEntry(entry)
		switch mode {
		case ServerDeviceModeWG:
			if fields.IP != "" && fields.IP != DefaultWdttServerGatewayAddr {
				out[id] = true
			}
		case ServerDeviceModeRaw:
			if fields.RawIP != "" {
				out[id] = true
			}
		}
	}
	return out
}

func mergeDeviceActivity(devices []ServerDeviceEntry, activeIDs map[string]bool, known bool) []ServerDeviceEntry {
	if len(devices) == 0 {
		return devices
	}
	out := make([]ServerDeviceEntry, len(devices))
	copy(out, devices)
	for i := range out {
		out[i].ActiveKnown = known
		if known {
			out[i].Active = activeIDs[out[i].DeviceID]
		}
	}
	return out
}

func countActiveDevices(devices []ServerDeviceEntry) int {
	n := 0
	for _, d := range devices {
		if d.Active {
			n++
		}
	}
	return n
}
