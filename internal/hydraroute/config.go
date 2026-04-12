package hydraroute

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ReadConfig parses hrneo.conf and returns the managed Config fields.
// Unknown keys and comments are ignored; defaults are applied where needed.
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

		switch key {
		case "AutoStart":
			cfg.AutoStart = parseBool(val)
		case "ClearIPSet":
			cfg.ClearIPSet = parseBool(val)
		case "CIDR":
			cfg.CIDR = parseBool(val)
		case "IpsetEnableTimeout":
			cfg.IpsetEnableTimeout = parseBool(val)
		case "IpsetTimeout":
			cfg.IpsetTimeout, _ = strconv.Atoi(val)
		case "IpsetMaxElem":
			cfg.IpsetMaxElem, _ = strconv.Atoi(val)
		case "DirectRouteEnabled":
			cfg.DirectRouteEnabled = parseBool(val)
		case "GlobalRouting":
			cfg.GlobalRouting = parseBool(val)
		case "ConntrackFlush":
			cfg.ConntrackFlush = parseBool(val)
		case "Log":
			cfg.Log = val
		case "LogFile":
			cfg.LogFile = val
		case "GeoIPFile":
			if val != "" {
				cfg.GeoIPFiles = append(cfg.GeoIPFiles, val)
			}
		case "GeoSiteFile":
			if val != "" {
				cfg.GeoSiteFiles = append(cfg.GeoSiteFiles, val)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("hydraroute: scan hrneo.conf: %w", err)
	}
	return cfg, nil
}

// WriteConfig updates hrneo.conf with the managed Config fields, preserving
// unknown keys and comments. Multi-value fields (GeoIPFile, GeoSiteFile) are
// written in full on the first occurrence; subsequent original lines are dropped.
func WriteConfig(cfg *Config) error {
	if err := os.MkdirAll(hrDir, 0o755); err != nil {
		return fmt.Errorf("hydraroute: create hrneo dir: %w", err)
	}

	existing, err := os.ReadFile(hrConfPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("hydraroute: read hrneo.conf: %w", err)
	}

	// Track which known keys have been written (for in-place replacement).
	type knownKey struct {
		written bool // first occurrence replaced, subsequent dropped
	}
	knownKeys := map[string]*knownKey{
		"AutoStart":          {},
		"ClearIPSet":         {},
		"CIDR":               {},
		"IpsetEnableTimeout": {},
		"IpsetTimeout":       {},
		"IpsetMaxElem":       {},
		"DirectRouteEnabled": {},
		"GlobalRouting":      {},
		"ConntrackFlush":     {},
		"Log":                {},
		"LogFile":            {},
		"GeoIPFile":          {},
		"GeoSiteFile":        {},
	}

	var out strings.Builder

	if len(existing) > 0 {
		scanner := bufio.NewScanner(strings.NewReader(string(existing)))
		for scanner.Scan() {
			rawLine := scanner.Text()

			// Determine the key (stripping comments for detection).
			stripped := rawLine
			if idx := strings.Index(stripped, "#"); idx >= 0 {
				stripped = stripped[:idx]
			}
			stripped = strings.TrimSpace(stripped)

			key := ""
			if k, _, ok := strings.Cut(stripped, "="); ok {
				key = strings.TrimSpace(k)
			}

			state, isKnown := knownKeys[key]
			if !isKnown {
				// Preserve unknown lines as-is.
				out.WriteString(rawLine)
				out.WriteByte('\n')
				continue
			}

			if state.written {
				// Drop subsequent occurrences of multi-value keys.
				continue
			}
			state.written = true

			// Replace with new value(s).
			switch key {
			case "GeoIPFile":
				for _, v := range cfg.GeoIPFiles {
					fmt.Fprintf(&out, "GeoIPFile=%s\n", v)
				}
				if len(cfg.GeoIPFiles) == 0 {
					out.WriteString("GeoIPFile=\n")
				}
			case "GeoSiteFile":
				for _, v := range cfg.GeoSiteFiles {
					fmt.Fprintf(&out, "GeoSiteFile=%s\n", v)
				}
				if len(cfg.GeoSiteFiles) == 0 {
					out.WriteString("GeoSiteFile=\n")
				}
			default:
				fmt.Fprintf(&out, "%s=%s\n", key, configValue(key, cfg))
			}
		}
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("hydraroute: scan hrneo.conf: %w", err)
		}
	}

	// Append any known keys that were not present in the original file.
	appendIfMissing := func(key string, value string) {
		if state := knownKeys[key]; !state.written {
			fmt.Fprintf(&out, "%s=%s\n", key, value)
			state.written = true
		}
	}

	appendIfMissing("AutoStart", formatBool(cfg.AutoStart))
	appendIfMissing("ClearIPSet", formatBool(cfg.ClearIPSet))
	appendIfMissing("CIDR", formatBool(cfg.CIDR))
	appendIfMissing("IpsetEnableTimeout", formatBool(cfg.IpsetEnableTimeout))
	appendIfMissing("IpsetTimeout", strconv.Itoa(cfg.IpsetTimeout))
	appendIfMissing("IpsetMaxElem", strconv.Itoa(cfg.IpsetMaxElem))
	appendIfMissing("DirectRouteEnabled", formatBool(cfg.DirectRouteEnabled))
	appendIfMissing("GlobalRouting", formatBool(cfg.GlobalRouting))
	appendIfMissing("ConntrackFlush", formatBool(cfg.ConntrackFlush))
	appendIfMissing("Log", cfg.Log)
	appendIfMissing("LogFile", cfg.LogFile)
	if state := knownKeys["GeoIPFile"]; !state.written {
		for _, v := range cfg.GeoIPFiles {
			fmt.Fprintf(&out, "GeoIPFile=%s\n", v)
		}
		if len(cfg.GeoIPFiles) == 0 {
			out.WriteString("GeoIPFile=\n")
		}
		state.written = true
	}
	if state := knownKeys["GeoSiteFile"]; !state.written {
		for _, v := range cfg.GeoSiteFiles {
			fmt.Fprintf(&out, "GeoSiteFile=%s\n", v)
		}
		if len(cfg.GeoSiteFiles) == 0 {
			out.WriteString("GeoSiteFile=\n")
		}
		state.written = true
	}

	return atomicWrite(hrConfPath, out.String())
}

// configValue returns the string representation for a scalar known key.
func configValue(key string, cfg *Config) string {
	switch key {
	case "AutoStart":
		return formatBool(cfg.AutoStart)
	case "ClearIPSet":
		return formatBool(cfg.ClearIPSet)
	case "CIDR":
		return formatBool(cfg.CIDR)
	case "IpsetEnableTimeout":
		return formatBool(cfg.IpsetEnableTimeout)
	case "IpsetTimeout":
		return strconv.Itoa(cfg.IpsetTimeout)
	case "IpsetMaxElem":
		return strconv.Itoa(cfg.IpsetMaxElem)
	case "DirectRouteEnabled":
		return formatBool(cfg.DirectRouteEnabled)
	case "GlobalRouting":
		return formatBool(cfg.GlobalRouting)
	case "ConntrackFlush":
		return formatBool(cfg.ConntrackFlush)
	case "Log":
		return cfg.Log
	case "LogFile":
		return cfg.LogFile
	}
	return ""
}

// defaultConfig returns a Config with sensible defaults.
func defaultConfig() *Config {
	return &Config{
		DirectRouteEnabled: true,
		ConntrackFlush:     true,
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
