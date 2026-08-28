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
	// AWG 3.1: the two device flags. Checked before awg3 — 3.1 is a superset,
	// so a config carrying both a 3.0 param and a flag is a 3.1 config.
	if hasAnyAWG31Param(iface) {
		return "awg3.1"
	}
	// AWG 3.0: any awg3 device param (header protection / content padding /
	// timing range). awg3 builds on top of the AWG 1.x/2.0 obfuscation, so its
	// presence outranks H-ranges and signature packets.
	if hasAnyAWG3Param(iface) {
		return "awg3"
	}
	// AWG 2.0: any H-value is a numeric range "min-max"
	if isRange(iface.H1) || isRange(iface.H2) || isRange(iface.H3) || isRange(iface.H4) {
		return "awg2.0"
	}
	// AWG 1.5: has any signature packet slot I1-I5
	if hasAnySignaturePacket(iface) {
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

func hasAnySignaturePacket(iface *storage.AWGInterface) bool {
	if iface == nil {
		return false
	}
	return iface.I1 != "" || iface.I2 != "" || iface.I3 != "" ||
		iface.I4 != "" || iface.I5 != ""
}

// hasAnyAWG3Param reports whether any AmneziaWG 3.0 device parameter is set
// (header protection, content padding, or one of the timing ranges).
func hasAnyAWG3Param(iface *storage.AWGInterface) bool {
	if iface == nil {
		return false
	}
	return iface.HeaderProtectionKey != "" || iface.ContentPaddingAddition != "" ||
		iface.RekeyAfterTime != "" || iface.RekeyTimeout != "" ||
		iface.RejectAfterTime != "" || iface.KeepaliveTimeout != "" ||
		iface.MaxHandshakeAttempts != ""
}

// hasAnyAWG31Param reports whether one of the AmneziaWG 3.1 device flags is set.
func hasAnyAWG31Param(iface *storage.AWGInterface) bool {
	if iface == nil {
		return false
	}
	return iface.RandomTrailers || iface.DisableCookies
}
