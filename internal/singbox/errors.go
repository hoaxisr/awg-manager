package singbox

import (
	"errors"

	"github.com/hoaxisr/awg-manager/internal/singbox/installer"
)

// ErrTunnelNotFound is returned when a tunnel tag does not exist in config.json.
var ErrTunnelNotFound = errors.New("tunnel not found")

// ErrTunnelTagConflict is returned when a requested tunnel tag is already used.
var ErrTunnelTagConflict = errors.New("tunnel tag already exists")

// ErrInvalidTunnelTag is returned when a requested tunnel tag cannot be used.
var ErrInvalidTunnelTag = errors.New("invalid tunnel tag")

// ErrSingboxNotRunning is returned by operations that require a live
// sing-box process when the daemon is down. Callers that want
// best-effort semantics (e.g. deviceproxy runtime switch persists to
// config.json either way) should check for this explicitly.
var ErrSingboxNotRunning = errors.New("sing-box is not running")

// ErrInstallInProgress is returned by Install/Update when another install
// or update is already in flight — guards against manual and scheduled
// binary installs interleaving.
var ErrInstallInProgress = errors.New("sing-box install already in progress")

// ErrNoSpace is returned by Install/Update when the pinned binary doesn't fit:
// the disk-gate refused or the write hit ENOSPC. Details (required/free) are in
// GetStatus.
var ErrNoSpace = installer.ErrNoSpace
