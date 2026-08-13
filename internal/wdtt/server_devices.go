package wdtt

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
)

const (
	ServerDeviceModeWG  = "wg"
	ServerDeviceModeRaw = "raw"

	gatewayReservedDeviceID = "__awgm_gateway_reserved__"
)

type passwordsDeviceFields struct {
	IP        string
	RawIP     string
	Comment   string
	DownBytes int64
	UpBytes   int64
}

func parsePasswordsDeviceEntry(v any) passwordsDeviceFields {
	var out passwordsDeviceFields
	switch d := v.(type) {
	case map[string]any:
		out.IP = stringField(d, "ip")
		out.RawIP = stringField(d, "raw_ip", "rawIp")
		out.Comment = stringField(d, "comment")
		out.DownBytes = int64Field(d, "down_bytes", "downBytes")
		out.UpBytes = int64Field(d, "up_bytes", "upBytes")
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return out
		}
		var m map[string]json.RawMessage
		if json.Unmarshal(b, &m) != nil {
			return out
		}
		out.IP = rawStringField(m, "ip")
		out.RawIP = rawStringField(m, "raw_ip", "rawIp")
		out.Comment = rawStringField(m, "comment")
		out.DownBytes = rawInt64Field(m, "down_bytes", "downBytes")
		out.UpBytes = rawInt64Field(m, "up_bytes", "upBytes")
	}
	return out
}

func stringField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func int64Field(m map[string]any, keys ...string) int64 {
	for _, k := range keys {
		switch v := m[k].(type) {
		case float64:
			return int64(v)
		case int64:
			return v
		case int:
			return int64(v)
		}
	}
	return 0
}

func rawStringField(m map[string]json.RawMessage, keys ...string) string {
	for _, k := range keys {
		raw, ok := m[k]
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) == nil && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func rawInt64Field(m map[string]json.RawMessage, keys ...string) int64 {
	for _, k := range keys {
		raw, ok := m[k]
		if !ok {
			continue
		}
		var n int64
		if json.Unmarshal(raw, &n) == nil {
			return n
		}
		var f float64
		if json.Unmarshal(raw, &f) == nil {
			return int64(f)
		}
	}
	return 0
}

func passwordCommentForDevice(doc passwordsJSON, deviceID string) string {
	for _, u := range doc.Passwords {
		if u.DeviceID == deviceID {
			return strings.TrimSpace(u.Comment)
		}
		for _, id := range u.DeviceIDs {
			if id == deviceID {
				return strings.TrimSpace(u.Comment)
			}
		}
	}
	return ""
}

func listServerDevices(doc passwordsJSON, mode string) []ServerDeviceEntry {
	mode = strings.ToLower(strings.TrimSpace(mode))
	var out []ServerDeviceEntry
	for id, entry := range doc.Devices {
		if id == "" || id == gatewayReservedDeviceID {
			continue
		}
		fields := parsePasswordsDeviceEntry(entry)
		item := ServerDeviceEntry{
			DeviceID:        id,
			IP:              fields.IP,
			RawIP:           fields.RawIP,
			Comment:         fields.Comment,
			PasswordComment: passwordCommentForDevice(doc, id),
			DownBytes:       fields.DownBytes,
			UpBytes:         fields.UpBytes,
		}
		switch mode {
		case ServerDeviceModeRaw:
			if fields.RawIP == "" && fields.IP == "" && fields.Comment == "" {
				// Пустая заготовка без raw_ip — показываем как reserved.
				item.Reserved = true
			}
			if fields.RawIP != "" || item.Reserved || fields.Comment != "" {
				out = append(out, item)
			}
		case ServerDeviceModeWG:
			if fields.IP != "" && fields.IP != DefaultWdttServerGatewayAddr {
				out = append(out, item)
			} else if fields.IP == "" && fields.RawIP == "" && fields.Comment != "" {
				item.Reserved = true
				out = append(out, item)
			}
		default:
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		keyA := a.RawIP
		keyB := b.RawIP
		if mode == ServerDeviceModeWG {
			keyA, keyB = a.IP, b.IP
		}
		if keyA != keyB {
			return keyA < keyB
		}
		return a.DeviceID < b.DeviceID
	})
	return out
}

func savePasswordsJSONDoc(configDir string, doc passwordsJSON) error {
	dir := strings.TrimSpace(configDir)
	if dir == "" {
		return fmt.Errorf("config-dir не задан")
	}
	if doc.Passwords == nil {
		doc.Passwords = map[string]passwordsJSONUser{}
	}
	if doc.Devices == nil {
		doc.Devices = map[string]any{}
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(passwordsJSONPath(dir), data, 0600)
}

func collectUsedWGIPs(devices map[string]any, skipDeviceID string) map[string]bool {
	used := map[string]bool{DefaultWdttServerGatewayAddr: true}
	for id, entry := range devices {
		if id == skipDeviceID || id == gatewayReservedDeviceID {
			continue
		}
		if ip := parsePasswordsDeviceEntry(entry).IP; ip != "" {
			used[ip] = true
		}
	}
	return used
}

func collectUsedRawIPs(devices map[string]any, skipDeviceID string) map[string]bool {
	used := map[string]bool{DefaultRawServerAddr: true}
	for id, entry := range devices {
		if id == skipDeviceID || id == gatewayReservedDeviceID {
			continue
		}
		fields := parsePasswordsDeviceEntry(entry)
		if fields.RawIP != "" {
			used[fields.RawIP] = true
		}
	}
	return used
}

func validateWGClientIP(ip, skipDeviceID string, devices map[string]any) error {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return nil
	}
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() == nil {
		return fmt.Errorf("некорректный IPv4: %s", ip)
	}
	if ip == DefaultWdttServerGatewayAddr {
		return fmt.Errorf("%s — адрес шлюза сервера, клиентам недоступен", ip)
	}
	_, network, err := net.ParseCIDR(DefaultWdttClientPoolCIDR)
	if err != nil || !network.Contains(parsed) {
		return fmt.Errorf("IP должен быть в пуле %s", DefaultWdttClientPoolCIDR)
	}
	if used := collectUsedWGIPs(devices, skipDeviceID); used[ip] {
		return fmt.Errorf("IP %s уже занят другим устройством", ip)
	}
	return nil
}

func validateRawClientIP(ip, skipDeviceID string, devices map[string]any) error {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return nil
	}
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() == nil {
		return fmt.Errorf("некорректный IPv4: %s", ip)
	}
	if ip == DefaultRawServerAddr {
		return fmt.Errorf("%s — адрес raw-шлюза сервера", ip)
	}
	_, network, err := net.ParseCIDR("10.70.0.0/16")
	if err != nil || !network.Contains(parsed) {
		return fmt.Errorf("raw IP должен быть в сети 10.70.0.0/16")
	}
	if used := collectUsedRawIPs(devices, skipDeviceID); used[ip] {
		return fmt.Errorf("raw IP %s уже занят другим устройством", ip)
	}
	return nil
}

func upsertPasswordsDevice(devices map[string]any, deviceID string, mutate func(map[string]any)) {
	if devices == nil {
		devices = map[string]any{}
	}
	cur, _ := devices[deviceID].(map[string]any)
	if cur == nil {
		cur = map[string]any{}
	}
	mutate(cur)
	devices[deviceID] = cur
}

func removePasswordsDevice(doc *passwordsJSON, deviceID string) {
	delete(doc.Devices, deviceID)
	for pass, u := range doc.Passwords {
		if u.DeviceID == deviceID {
			u.DeviceID = ""
			doc.Passwords[pass] = u
		}
		if len(u.DeviceIDs) > 0 {
			filtered := u.DeviceIDs[:0]
			for _, id := range u.DeviceIDs {
				if id != deviceID {
					filtered = append(filtered, id)
				}
			}
			u.DeviceIDs = filtered
			doc.Passwords[pass] = u
		}
	}
}

func unbindPasswordsDevice(doc *passwordsJSON, deviceID string) {
	for pass, u := range doc.Passwords {
		changed := false
		if u.DeviceID == deviceID {
			u.DeviceID = ""
			changed = true
		}
		if len(u.DeviceIDs) > 0 {
			filtered := u.DeviceIDs[:0]
			for _, id := range u.DeviceIDs {
				if id != deviceID {
					filtered = append(filtered, id)
				}
			}
			if len(filtered) != len(u.DeviceIDs) {
				u.DeviceIDs = filtered
				changed = true
			}
		}
		if changed {
			doc.Passwords[pass] = u
		}
	}
}
