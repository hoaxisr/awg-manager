package main

import (
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
)

// buildArch is set via ldflags at build time to one of the awg-manager
// architecture keys: "mipsel-3.4" | "mips-3.4" | "aarch64-3.10".
// Empty when running `go run` / `go build ./cmd/awg-manager` directly —
// detectArch() falls back to runtime.GOARCH-based mapping.
var buildArch string

// detectArch returns the awg-manager arch key for installer.EmbeddedBinaries.
// Prefers the build-time -X main.buildArch override; falls back to
// runtime.GOARCH for dev builds.
func detectArch() string {
	if buildArch != "" {
		return buildArch
	}
	switch runtime.GOARCH {
	case "mipsle":
		return "mipsel-3.4"
	case "mips":
		return "mips-3.4"
	case "arm64":
		return "aarch64-3.10"
	}
	return ""
}

// applyGoMemoryLimits tunes THIS process's GC via runtime/debug. Two things
// deliberately do NOT happen here:
//   - no os.Setenv: GOGC/GOMEMLIMIT env vars are read once at runtime init,
//     so setting them from main() is a no-op for the current process — and
//     the mutated env would be inherited by the spawned sing-box, overriding
//     its own deliberate defaults (GOGC=75/GOMEMLIMIT=128MiB, see
//     internal/singbox/process.go) with the manager's much tighter limits.
//   - explicit env vars still win: a user-provided GOGC/GOMEMLIMIT was
//     already applied by the runtime at startup, so we skip that knob.
func applyGoMemoryLimits(disableMemorySaving bool) {
	for _, entry := range osdetect.GetGCEnv(disableMemorySaving) {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 || os.Getenv(parts[0]) != "" {
			continue
		}
		switch parts[0] {
		case "GOGC":
			if pct, err := strconv.Atoi(parts[1]); err == nil {
				debug.SetGCPercent(pct)
			}
		case "GOMEMLIMIT":
			if limit, err := parseByteSize(parts[1]); err == nil {
				debug.SetMemoryLimit(limit)
			}
		}
	}
}

// parseByteSize parses the GOMEMLIMIT syntax subset osdetect emits ("16MiB").
func parseByteSize(s string) (int64, error) {
	n := strings.TrimSuffix(s, "MiB")
	if n == s {
		return 0, fmt.Errorf("unsupported byte size %q", s)
	}
	mib, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
	if err != nil {
		return 0, err
	}
	return mib << 20, nil
}

// getUptime reads system uptime in seconds from /proc/uptime.
// Returns 0 on error (treated as non-boot scenario).
func getUptime() float64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0
	}
	uptime, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return uptime
}

// ensureCACerts sets SSL_CERT_FILE for entware-based systems (Keenetic) where
// CA certificates live in /opt/etc/ssl/ instead of standard Linux paths.
// Without this, Go's crypto/tls fails to verify GitHub (and other) certificates.
func ensureCACerts() {
	if os.Getenv("SSL_CERT_FILE") != "" {
		return
	}
	const entwareCert = "/opt/etc/ssl/certs/ca-certificates.crt"
	if _, err := os.Stat(entwareCert); err == nil {
		os.Setenv("SSL_CERT_FILE", entwareCert)
	}
}

// ensureServiceEnv ensures PATH contains Entware and system directories so
// child processes can find binaries by name. Entware dirs go FIRST: a bare
// "ip" must resolve to iproute2 (/opt/sbin/ip), not the firmware busybox
// applet that lacks `ip rule`/`route show table` features. The guard checks
// for /opt/bin specifically — an ndm-hook environment can already contain
// /usr/sbin yet miss the Entware dirs entirely.
// LD_LIBRARY_PATH is intentionally NOT set: forcing /lib:/usr/lib first
// poisons Entware binaries (curl/openssl) by making ld.so load incompatible
// system libraries → SIGSEGV/SIGBUS at runtime.
func ensureServiceEnv() {
	path := os.Getenv("PATH")
	if !strings.Contains(path, "/opt/bin") || !strings.Contains(path, "/usr/sbin") {
		os.Setenv("PATH", "/opt/bin:/opt/sbin:/bin:/sbin:/usr/bin:/usr/sbin:"+path)
	}
}

// uaArch returns the arch tag for the User-Agent: the build key when ldflags
// set it, otherwise the Go arch so a dev binary is still identifiable.
func uaArch() string {
	if a := detectArch(); a != "" {
		return a
	}
	return runtime.GOARCH
}
