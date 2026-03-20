package ndms

import "regexp"

var wireguardNamePattern = regexp.MustCompile(`^Wireguard\d+$`)

// IsValidWireguardName checks that the name matches "WireguardN" pattern.
// Used to prevent command injection in ndmc calls.
func IsValidWireguardName(name string) bool {
	return wireguardNamePattern.MatchString(name)
}
