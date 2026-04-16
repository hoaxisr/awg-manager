package hydraroute

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Paths are package-level vars so tests can override them via t.TempDir().
var (
	domainConfPath = "/opt/etc/HydraRoute/domain.conf" //nolint:gochecknoglobals
	ipListPath     = "/opt/etc/HydraRoute/ip.list"     //nolint:gochecknoglobals
)

// SetPaths overrides the HR config file paths. Intended for tests that need
// to point the service at a temp directory; returns a restore function.
func SetPaths(domain, ipList string) (restore func()) {
	origDomain, origIP := domainConfPath, ipListPath
	domainConfPath = domain
	ipListPath = ipList
	return func() {
		domainConfPath = origDomain
		ipListPath = origIP
	}
}

// GenerateDomainConf produces the full domain.conf content. We own the
// whole file — no markers, no preserved user blocks. Format per entry:
//
//	## Name
//	domain1,domain2,geosite:TAG/Target
func GenerateDomainConf(lists []ManagedEntry) string {
	var sb strings.Builder
	for _, e := range lists {
		if len(e.Domains) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "## %s\n", e.ListName)
		fmt.Fprintf(&sb, "%s/%s\n", strings.Join(e.Domains, ","), e.Iface)
	}
	return sb.String()
}

// GenerateIPList produces the full ip.list content. Format per entry:
//
//	## Name
//	/Target
//	cidr1
//	cidr2
//	<empty line>
func GenerateIPList(lists []ManagedEntry) string {
	var sb strings.Builder
	for _, e := range lists {
		if len(e.Subnets) == 0 {
			continue
		}
		fmt.Fprintf(&sb, "## %s\n", e.ListName)
		fmt.Fprintf(&sb, "/%s\n", e.Iface)
		for _, s := range e.Subnets {
			sb.WriteString(s)
			sb.WriteByte('\n')
		}
		sb.WriteByte('\n') // HR Neo block terminator
	}
	return sb.String()
}

// WriteWholeFile atomically writes content as the entire file body.
// We own the file — anything outside our format is discarded on next write.
func WriteWholeFile(filePath, content string) error {
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return fmt.Errorf("hydraroute: create parent dir: %w", err)
	}
	return atomicWrite(filePath, content)
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
