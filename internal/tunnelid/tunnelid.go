// Package tunnelid holds the one definition of what a tunnel identifier
// may look like. IDs are minted by the store ("awg12", "nwg3") and are
// also user-controlled input on every REST and MCP call, where they end
// up in a file path (<dataDir>/tunnels/<id>.json). The check therefore
// lives in a leaf package with no dependencies so the HTTP layer, the MCP
// tools layer and the store itself can all import it — the MCP package
// stays portable and the store no longer trusts its callers.
package tunnelid

import "regexp"

// pattern: a letter followed by up to 31 letters, digits, hyphens or
// underscores. No dots, no slashes — an ID can never leave its directory.
var pattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,31}$`)

// Valid reports whether id is a well-formed tunnel identifier.
func Valid(id string) bool { return pattern.MatchString(id) }
