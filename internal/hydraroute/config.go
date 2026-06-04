package hydraroute

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// canonicalKey maps the lowercase form of each managed key to the
// casing hr-neo itself writes in /opt/etc/HydraRoute/hrneo.conf.
// Used only when appending a key that did not exist in the original
// file — for in-place rewrites we preserve whatever case the existing
// line used. Without this mapping our writer would invent a different
// case for the few keys where the daemon's convention isn't
// PascalCase (autoStart, clearIPSet, log, logfile), and the next
// daemon write would create a case-mismatched duplicate that breaks
// both reads and writes.
var canonicalKey = map[string]string{
	"autostart":          "autoStart",
	"clearipset":         "clearIPSet",
	"cidr":               "CIDR",
	"ipsetenabletimeout": "IpsetEnableTimeout",
	"ipsettimeout":       "IpsetTimeout",
	"ipsetmaxelem":       "IpsetMaxElem",
	"directrouteenabled": "DirectRouteEnabled",
	"globalrouting":      "GlobalRouting",
	"conntrackflush":     "ConntrackFlush",
	"log":                "log",
	"logfile":            "logfile",
	"geoipfile":          "GeoIPFile",
	"geositefile":        "GeoSiteFile",
	"policyorder":        "PolicyOrder",
}

// ReadConfig parses hrneo.conf and returns the managed Config fields.
// Unknown keys and comments are ignored; defaults are applied where
// needed. Key matching is case-insensitive — hr-neo's own writes mix
// PascalCase, camelCase, mixedCase, and lowercase for different
// fields, so we accept any casing and let WriteConfig dedupe.
func ReadConfig() (*Config, error) {
	f, err := os.Open(hrConfPath)
	if err != nil {
		if os.IsNotExist(err) {
			return defaultConfig(), nil
		}
		return nil, fmt.Errorf("hydraroute: open hrneo.conf: %w", err)
	}
	defer f.Close()

	cfg := defaultConfig()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// Strip inline comments (# not at start)
		if idx := strings.Index(line, "#"); idx >= 0 {
			line = line[:idx]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)

		switch strings.ToLower(key) {
		case "autostart":
			cfg.AutoStart = parseBool(val)
		case "clearipset":
			cfg.ClearIPSet = parseBool(val)
		case "cidr":
			cfg.CIDR = parseBool(val)
		case "ipsetenabletimeout":
			cfg.IpsetEnableTimeout = parseBool(val)
		case "ipsettimeout":
			cfg.IpsetTimeout, _ = strconv.Atoi(val)
		case "ipsetmaxelem":
			cfg.IpsetMaxElem, _ = strconv.Atoi(val)
		case "directrouteenabled":
			cfg.DirectRouteEnabled = parseBool(val)
		case "globalrouting":
			cfg.GlobalRouting = parseBool(val)
		case "conntrackflush":
			cfg.ConntrackFlush = parseBool(val)
		case "log":
			cfg.Log = val
		case "logfile":
			cfg.LogFile = val
		case "geoipfile":
			if val != "" {
				cfg.GeoIPFiles = append(cfg.GeoIPFiles, val)
			}
		case "geositefile":
			if val != "" {
				cfg.GeoSiteFiles = append(cfg.GeoSiteFiles, val)
			}
		case "policyorder":
			cfg.PolicyOrder = nil
			for _, s := range strings.Split(val, ",") {
				s = strings.TrimSpace(s)
				if s != "" {
					cfg.PolicyOrder = append(cfg.PolicyOrder, s)
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("hydraroute: scan hrneo.conf: %w", err)
	}
	return cfg, nil
}

// WriteConfig updates hrneo.conf with the managed Config fields,
// preserving unknown keys, comments, and the EXACT casing each
// existing managed key already uses on disk. Subsequent occurrences
// of the same case-insensitive key are dropped, which heals files
// previously corrupted by the older case-sensitive writer (it left
// behind a daemon-cased line AND a PascalCase appended duplicate).
// Multi-value fields (GeoIPFile, GeoSiteFile) are written in full on
// the first occurrence; subsequent occurrences are dropped.
// confLineKey extracts the config key from a raw hrneo.conf line, stripping any
// inline #-comment and surrounding space; returns "" for blank/comment/no-'='
// lines. Shared by the hrneo.conf rewriters so the comment-strip + key-extract
// rule lives in one place (the caller preserves origKey casing on write-back).
func confLineKey(rawLine string) string {
	stripped := rawLine
	if idx := strings.Index(stripped, "#"); idx >= 0 {
		stripped = stripped[:idx]
	}
	stripped = strings.TrimSpace(stripped)
	if k, _, ok := strings.Cut(stripped, "="); ok {
		return strings.TrimSpace(k)
	}
	return ""
}

func WriteConfig(cfg *Config) error {
	if err := os.MkdirAll(hrDir, 0o755); err != nil {
		return fmt.Errorf("hydraroute: create hrneo dir: %w", err)
	}
	cfg = normalizeConfigForWrite(cfg)

	existing, err := os.ReadFile(hrConfPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("hydraroute: read hrneo.conf: %w", err)
	}

	// keyState is keyed by lowercase key, so any casing in the file is
	// recognised and the first occurrence (whatever its case) is the
	// one that survives.
	type keyState struct{ written bool }
	known := make(map[string]*keyState, len(canonicalKey))
	for k := range canonicalKey {
		known[k] = &keyState{}
	}

	var out strings.Builder

	if len(existing) > 0 {
		scanner := bufio.NewScanner(strings.NewReader(string(existing)))
		for scanner.Scan() {
			rawLine := scanner.Text()

			// Detect the key — strip comment for matching, but the
			// in-place replacement preserves the original raw form
			// when we write back.
			origKey := confLineKey(rawLine)

			lowerKey := strings.ToLower(origKey)
			state, isKnown := known[lowerKey]
			if !isKnown {
				// Preserve unknown lines as-is.
				out.WriteString(rawLine)
				out.WriteByte('\n')
				continue
			}

			if state.written {
				// Drop case-insensitive duplicates (heals files
				// where the old case-sensitive writer left both a
				// daemon-cased and a PascalCase line for the same key).
				continue
			}
			state.written = true

			// Replace with new value(s), preserving the original
			// key casing observed in this line.
			switch lowerKey {
			case "geoipfile":
				for _, v := range cfg.GeoIPFiles {
					fmt.Fprintf(&out, "%s=%s\n", origKey, v)
				}
				if len(cfg.GeoIPFiles) == 0 {
					fmt.Fprintf(&out, "%s=\n", origKey)
				}
			case "geositefile":
				for _, v := range cfg.GeoSiteFiles {
					fmt.Fprintf(&out, "%s=%s\n", origKey, v)
				}
				if len(cfg.GeoSiteFiles) == 0 {
					fmt.Fprintf(&out, "%s=\n", origKey)
				}
			default:
				fmt.Fprintf(&out, "%s=%s\n", origKey, configValue(lowerKey, cfg))
			}
		}
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("hydraroute: scan hrneo.conf: %w", err)
		}
	}

	// Append any managed keys absent from the original file using
	// canonicalKey to pick the casing hr-neo itself would have used.
	appendIfMissing := func(lowerKey string, value string) {
		state := known[lowerKey]
		if state.written {
			return
		}
		fmt.Fprintf(&out, "%s=%s\n", canonicalKey[lowerKey], value)
		state.written = true
	}

	appendIfMissing("autostart", formatBool(cfg.AutoStart))
	appendIfMissing("clearipset", formatBool(cfg.ClearIPSet))
	appendIfMissing("cidr", formatBool(cfg.CIDR))
	appendIfMissing("ipsetenabletimeout", formatBool(cfg.IpsetEnableTimeout))
	appendIfMissing("ipsettimeout", strconv.Itoa(cfg.IpsetTimeout))
	appendIfMissing("ipsetmaxelem", strconv.Itoa(cfg.IpsetMaxElem))
	appendIfMissing("directrouteenabled", formatBool(cfg.DirectRouteEnabled))
	appendIfMissing("globalrouting", formatBool(cfg.GlobalRouting))
	appendIfMissing("conntrackflush", formatBool(cfg.ConntrackFlush))
	appendIfMissing("log", cfg.Log)
	appendIfMissing("logfile", cfg.LogFile)
	appendIfMissing("policyorder", strings.Join(cfg.PolicyOrder, ","))
	if state := known["geoipfile"]; !state.written {
		for _, v := range cfg.GeoIPFiles {
			fmt.Fprintf(&out, "%s=%s\n", canonicalKey["geoipfile"], v)
		}
		if len(cfg.GeoIPFiles) == 0 {
			fmt.Fprintf(&out, "%s=\n", canonicalKey["geoipfile"])
		}
		state.written = true
	}
	if state := known["geositefile"]; !state.written {
		for _, v := range cfg.GeoSiteFiles {
			fmt.Fprintf(&out, "%s=%s\n", canonicalKey["geositefile"], v)
		}
		if len(cfg.GeoSiteFiles) == 0 {
			fmt.Fprintf(&out, "%s=\n", canonicalKey["geositefile"])
		}
		state.written = true
	}

	return atomicWrite(hrConfPath, out.String())
}

// WritePolicyOrderOnly updates only PolicyOrder in hrneo.conf and leaves every
// other line untouched.
func WritePolicyOrderOnly(order []string) error {
	return patchSingleScalarKey("policyorder", strings.Join(order, ","))
}

// WriteGeoFilesOnly updates only GeoIPFile/GeoSiteFile entries in hrneo.conf
// and leaves every other line untouched.
func WriteGeoFilesOnly(geoIP []string, geoSite []string) error {
	return patchMultiValueKeys([]string{"geoipfile", "geositefile"}, map[string][]string{
		"geoipfile":   geoIP,
		"geositefile": geoSite,
	})
}

func patchSingleScalarKey(lowerKey string, value string) error {
	targetKey := canonicalKey[lowerKey]
	if targetKey == "" {
		return fmt.Errorf("hydraroute: unknown config key %q", lowerKey)
	}
	if err := os.MkdirAll(hrDir, 0o755); err != nil {
		return fmt.Errorf("hydraroute: create hrneo dir: %w", err)
	}
	existing, err := os.ReadFile(hrConfPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("hydraroute: read hrneo.conf: %w", err)
	}

	var out strings.Builder
	written := false

	if len(existing) > 0 {
		scanner := bufio.NewScanner(strings.NewReader(string(existing)))
		for scanner.Scan() {
			rawLine := scanner.Text()
			origKey := confLineKey(rawLine)
			if strings.EqualFold(origKey, targetKey) {
				if !written {
					fmt.Fprintf(&out, "%s=%s\n", origKey, value)
					written = true
				}
				continue
			}
			out.WriteString(rawLine)
			out.WriteByte('\n')
		}
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("hydraroute: scan hrneo.conf: %w", err)
		}
	}
	if !written {
		fmt.Fprintf(&out, "%s=%s\n", targetKey, value)
	}
	return atomicWrite(hrConfPath, out.String())
}

func patchMultiValueKey(lowerKey string, values []string) error {
	return patchMultiValueKeys([]string{lowerKey}, map[string][]string{lowerKey: values})
}

func patchMultiValueKeys(order []string, updates map[string][]string) error {
	for k := range updates {
		found := false
		for _, orderedKey := range order {
			if k == orderedKey {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("hydraroute: config key %q missing from patch order", k)
		}
	}
	for _, k := range order {
		if _, ok := updates[k]; !ok {
			return fmt.Errorf("hydraroute: config key %q missing from patch updates", k)
		}
	}

	if err := os.MkdirAll(hrDir, 0o755); err != nil {
		return fmt.Errorf("hydraroute: create hrneo dir: %w", err)
	}
	existing, err := os.ReadFile(hrConfPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("hydraroute: read hrneo.conf: %w", err)
	}
	var out strings.Builder
	written := make(map[string]bool, len(updates))
	targetKeys := make(map[string]string, len(updates))
	for _, lowerKey := range order {
		targetKey, ok := canonicalKey[lowerKey]
		if !ok || targetKey == "" {
			return fmt.Errorf("hydraroute: unknown config key %q", lowerKey)
		}
		targetKeys[lowerKey] = targetKey
	}
	writeValues := func(lowerKey, key string) {
		values := updates[lowerKey]
		for _, v := range values {
			fmt.Fprintf(&out, "%s=%s\n", key, v)
		}
		if len(values) == 0 {
			fmt.Fprintf(&out, "%s=\n", key)
		}
	}

	if len(existing) > 0 {
		scanner := bufio.NewScanner(strings.NewReader(string(existing)))
		for scanner.Scan() {
			rawLine := scanner.Text()
			origKey := confLineKey(rawLine)
			for _, lowerKey := range order {
				targetKey := targetKeys[lowerKey]
				if strings.EqualFold(origKey, targetKey) {
					if !written[lowerKey] {
						writeValues(lowerKey, origKey)
						written[lowerKey] = true
					}
					goto nextLine
				}
			}
			out.WriteString(rawLine)
			out.WriteByte('\n')
		nextLine:
		}
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("hydraroute: scan hrneo.conf: %w", err)
		}
	}
	for _, lowerKey := range order {
		if !written[lowerKey] {
			writeValues(lowerKey, targetKeys[lowerKey])
		}
	}

	return atomicWrite(hrConfPath, out.String())
}

func normalizeConfigForWrite(cfg *Config) *Config {
	if cfg == nil {
		return &Config{IpsetMaxElem: defaultMaxElem}
	}
	cloned := *cfg
	if cloned.IpsetMaxElem <= 0 {
		cloned.IpsetMaxElem = defaultMaxElem
	}
	return &cloned
}

// HealInvalidRuntimeConfig patch-heals existing IpsetMaxElem keys in
// hrneo.conf. It collapses duplicates into one key (first-key casing kept),
// prefers the first valid value (>0), otherwise falls back to defaultMaxElem.
// It never adds IpsetMaxElem if absent.
func HealInvalidRuntimeConfig() (bool, int, error) {
	existing, err := os.ReadFile(hrConfPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, 0, nil
		}
		return false, 0, fmt.Errorf("hydraroute: read hrneo.conf: %w", err)
	}
	if len(existing) == 0 {
		return false, 0, nil
	}

	var outLines []string
	changed := false
	firstKey := ""
	firstVal := ""
	keyFound := false
	seenValid := false
	chosen := defaultMaxElem
	firstIdx := -1
	scanner := bufio.NewScanner(strings.NewReader(string(existing)))
	for scanner.Scan() {
		rawLine := scanner.Text()
		stripped := rawLine
		if idx := strings.Index(stripped, "#"); idx >= 0 {
			stripped = stripped[:idx]
		}
		stripped = strings.TrimSpace(stripped)

		key, val, ok := strings.Cut(stripped, "=")
		if !ok {
			outLines = append(outLines, rawLine)
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if strings.EqualFold(key, canonicalKey["ipsetmaxelem"]) {
			if !keyFound {
				keyFound = true
				firstKey = key
				firstVal = val
				firstIdx = len(outLines)
				outLines = append(outLines, rawLine)
			} else {
				changed = true // duplicate key removed
			}
			n, convErr := strconv.Atoi(val)
			if convErr == nil && n > 0 && !seenValid {
				chosen = n
				seenValid = true
			}
			continue
		}
		outLines = append(outLines, rawLine)
	}
	if err := scanner.Err(); err != nil {
		return false, 0, fmt.Errorf("hydraroute: scan hrneo.conf: %w", err)
	}
	if !keyFound {
		return false, 0, nil
	}
	if !seenValid {
		changed = true
	}
	if seenValid && strconv.Itoa(chosen) != strings.TrimSpace(firstVal) {
		changed = true
	}
	outLines[firstIdx] = fmt.Sprintf("%s=%d", firstKey, chosen)
	if !changed {
		return false, chosen, nil
	}
	if err := atomicWrite(hrConfPath, strings.Join(outLines, "\n")+"\n"); err != nil {
		return false, 0, err
	}
	return true, chosen, nil
}

// configValue returns the string representation for a scalar managed key.
// lowerKey must already be lowercased.
func configValue(lowerKey string, cfg *Config) string {
	switch lowerKey {
	case "autostart":
		return formatBool(cfg.AutoStart)
	case "clearipset":
		return formatBool(cfg.ClearIPSet)
	case "cidr":
		return formatBool(cfg.CIDR)
	case "ipsetenabletimeout":
		return formatBool(cfg.IpsetEnableTimeout)
	case "ipsettimeout":
		return strconv.Itoa(cfg.IpsetTimeout)
	case "ipsetmaxelem":
		return strconv.Itoa(cfg.IpsetMaxElem)
	case "directrouteenabled":
		return formatBool(cfg.DirectRouteEnabled)
	case "globalrouting":
		return formatBool(cfg.GlobalRouting)
	case "conntrackflush":
		return formatBool(cfg.ConntrackFlush)
	case "log":
		return cfg.Log
	case "logfile":
		return cfg.LogFile
	case "policyorder":
		return strings.Join(cfg.PolicyOrder, ",")
	}
	return ""
}

// defaultConfig returns a Config with sensible defaults.
func defaultConfig() *Config {
	return &Config{
		DirectRouteEnabled: true,
		ConntrackFlush:     true,
		IpsetMaxElem:       defaultMaxElem,
	}
}

// parseBool returns true for "true", "1", or "yes" (case-insensitive).
func parseBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes":
		return true
	}
	return false
}

// formatBool returns "true" or "false".
func formatBool(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
