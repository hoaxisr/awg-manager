// internal/singbox/awgoutbounds/config.go
package awgoutbounds

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// fileShape is what 15-awg.json contains. Only the outbounds key is
// used — sing-box merges per-key across config.d/*.json so we
// deliberately avoid declaring inbounds/route/dns here.
type fileShape struct {
	Outbounds []map[string]any `json:"outbounds"`
}

// buildOutbounds projects entries into the JSON shape sing-box expects.
// One direct outbound per entry, bound to its kernel iface.
func buildOutbounds(entries []AWGEntry) []map[string]any {
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		out = append(out, map[string]any{
			"type":           "direct",
			"tag":            e.Tag,
			"bind_interface": e.Iface,
		})
	}
	return out
}

// saveFile writes 15-awg.json atomically (tmp + rename). Always emits
// a valid JSON object — even with zero entries the file contains
// `{"outbounds":[]}` so sing-box can still merge config.d cleanly.
func saveFile(path string, entries []AWGEntry) error {
	f := fileShape{Outbounds: buildOutbounds(entries)}
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("mkdir parent: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0644); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
