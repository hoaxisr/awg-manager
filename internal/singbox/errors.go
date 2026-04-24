package singbox

import (
	"errors"
	"fmt"
)

// ErrTunnelNotFound is returned when a tunnel tag does not exist in config.json.
var ErrTunnelNotFound = errors.New("tunnel not found")

// ErrSingboxNotRunning is returned by operations that require a live
// sing-box process when the daemon is down. Callers that want
// best-effort semantics (e.g. deviceproxy runtime switch persists to
// config.json either way) should check for this explicitly.
var ErrSingboxNotRunning = fmt.Errorf("sing-box is not running")
