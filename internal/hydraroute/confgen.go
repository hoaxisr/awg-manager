package hydraroute

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	markerStart    = "## --- AWG Manager START ---"
	markerEnd      = "## --- AWG Manager END ---"
	domainConfPath = "/opt/etc/HydraRoute/domain.conf"
	ipListPath     = "/opt/etc/HydraRoute/ip.list"
)

// GenerateDomainConf produces the AWG-managed section for domain.conf.
// Format per entry (if it has domains):
//
//	## list:ID:Name
//	domain1,domain2,geosite:TAG/IfaceName
func GenerateDomainConf(lists []ManagedEntry) string {
	var sb strings.Builder
	sb.WriteString(markerStart)
	sb.WriteByte('\n')

	for _, e := range lists {
		if len(e.Domains) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "## list:%s:%s\n", e.ListID, e.ListName)
		fmt.Fprintf(&sb, "%s/%s\n", strings.Join(e.Domains, ","), e.Iface)
	}

	sb.WriteString(markerEnd)
	sb.WriteByte('\n')
	return sb.String()
}

// GenerateIPList produces the AWG-managed section for ip.list.
// Format per entry (if it has subnets):
//
//	##ListName
//	/IfaceName
//	cidr1
//	cidr2
//	<empty line>
func GenerateIPList(lists []ManagedEntry) string {
	var sb strings.Builder
	sb.WriteString(markerStart)
	sb.WriteByte('\n')

	for _, e := range lists {
		if len(e.Subnets) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "##%s\n", e.ListName)
		fmt.Fprintf(&sb, "/%s\n", e.Iface)
		for _, s := range e.Subnets {
			sb.WriteString(s)
			sb.WriteByte('\n')
		}
		sb.WriteByte('\n') // HRNeo block terminator
	}

	sb.WriteString(markerEnd)
	sb.WriteByte('\n')
	return sb.String()
}

// WriteManagedSection writes content into filePath, preserving any user content
// outside the AWG Manager markers.
//
//   - File doesn't exist → create with content only.
//   - File exists with markers → replace the section between markers (inclusive).
//   - File exists without markers → append content to end.
//
// Writes are atomic: content is first written to a temp file then renamed.
func WriteManagedSection(filePath, content string) error {
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return fmt.Errorf("hydraroute: create parent dir: %w", err)
	}

	existing, err := os.ReadFile(filePath)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("hydraroute: read %s: %w", filePath, err)
		}
		// File doesn't exist — create with content only.
		return atomicWrite(filePath, content)
	}

	text := string(existing)
	startIdx := strings.Index(text, markerStart)
	endIdx := strings.Index(text, markerEnd)

	if startIdx >= 0 && endIdx > startIdx {
		// Replace the section between (and including) the markers.
		after := endIdx + len(markerEnd)
		// Skip a single trailing newline after the end marker if present.
		if after < len(text) && text[after] == '\n' {
			after++
		}
		merged := text[:startIdx] + content + text[after:]
		return atomicWrite(filePath, merged)
	}

	// No markers found — append to end.
	merged := text + content
	return atomicWrite(filePath, merged)
}

func atomicWrite(filePath, content string) error {
	tmpPath := filePath + ".awgm.tmp"
	if err := os.WriteFile(tmpPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("hydraroute: write tmp: %w", err)
	}
	if err := os.Rename(tmpPath, filePath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("hydraroute: rename tmp: %w", err)
	}
	return nil
}
