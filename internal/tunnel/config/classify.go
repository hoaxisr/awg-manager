package config

import (
	"regexp"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

var rangePattern = regexp.MustCompile(`^\d+-\d+$`)

// ClassifyAWGVersion returns the AWG protocol version string
// based on the obfuscation parameters of the interface.
func ClassifyAWGVersion(iface *storage.AWGInterface) string {
	if iface == nil {
		return "wg"
	}
	// AWG 2.0: any H-value is a numeric range "min-max"
	if isRange(iface.H1) || isRange(iface.H2) || isRange(iface.H3) || isRange(iface.H4) {
		return "awg2.0"
	}
	// AWG 1.5: has signature packet I1
	if iface.I1 != "" {
		return "awg1.5"
	}
	// AWG 1.0: all four H-values set
	if iface.H1 != "" && iface.H2 != "" && iface.H3 != "" && iface.H4 != "" {
		return "awg1.0"
	}
	return "wg"
}

// IsAWGObfuscated returns true if the interface has AWG obfuscation parameters.
// Plain WireGuard configs (no jc/jmin/jmax/s/h values) return false.
func IsAWGObfuscated(iface *storage.AWGInterface) bool {
	return ClassifyAWGVersion(iface) != "wg"
}

func isRange(s string) bool {
	return s != "" && rangePattern.MatchString(s)
}
