package testing

import "errors"

var (
	ErrTunnelNotRunning = errors.New("tunnel not running")
	ErrInvalidTunnelID  = errors.New("invalid tunnel ID")
	ErrTunnelNotFound   = errors.New("tunnel not found")
)
