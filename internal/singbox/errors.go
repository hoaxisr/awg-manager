package singbox

import "errors"

// ErrTunnelNotFound is returned when a tunnel tag does not exist in config.json.
var ErrTunnelNotFound = errors.New("tunnel not found")
