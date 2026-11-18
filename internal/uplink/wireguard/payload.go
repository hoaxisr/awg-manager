package wireguard

import "strings"

// Payload may include IPv6; the outer endpoint and provider ownership remain
// strictly IPv4. These are the only supported full-tunnel peer shapes.
func validPayloadPrefixes(prefixes []string) bool {
	return len(prefixes) == 0 || (len(prefixes) == 1 && prefixes[0] == "0.0.0.0/0") || (len(prefixes) == 2 && prefixes[0] == "0.0.0.0/0" && prefixes[1] == "::/0")
}
func payloadPrefixes(prefixes []string) string {
	if len(prefixes) == 0 {
		return "0.0.0.0/0"
	}
	return strings.Join(prefixes, " ")
}
