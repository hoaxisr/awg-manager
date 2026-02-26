package tunnel

import (
	"errors"
	"fmt"
)

// Sentinel errors for tunnel operations.
var (
	// ErrNotFound indicates the tunnel does not exist in storage.
	ErrNotFound = errors.New("tunnel not found")

	// ErrAlreadyExists indicates attempting to create a tunnel that already exists.
	ErrAlreadyExists = errors.New("tunnel already exists")

	// ErrAlreadyRunning indicates attempting to start a tunnel that is already running.
	ErrAlreadyRunning = errors.New("tunnel already running")

	// ErrNotRunning indicates attempting to stop a tunnel that is not running.
	ErrNotRunning = errors.New("tunnel not running")

	// ErrBrokenState indicates the tunnel is in an inconsistent state.
	ErrBrokenState = errors.New("tunnel in broken state")

	// ErrTransitioning indicates the tunnel is currently starting or stopping.
	ErrTransitioning = errors.New("tunnel is transitioning")

	// ErrInvalidConfig indicates the tunnel configuration is invalid.
	ErrInvalidConfig = errors.New("invalid configuration")

	// ErrOperationFailed indicates a tunnel operation failed.
	ErrOperationFailed = errors.New("operation failed")

	// ErrTimeout indicates an operation timed out.
	ErrTimeout = errors.New("operation timeout")

	// ErrBackendNotSupported indicates the backend type is not supported.
	ErrBackendNotSupported = errors.New("backend not supported")

	// ErrAddressInUse indicates the tunnel address is already assigned to a system interface.
	ErrAddressInUse = errors.New("address already in use")
)

// OpError represents an error that occurred during a tunnel operation.
// It provides context about which operation failed and in which component.
type OpError struct {
	Op        string // Operation: "create", "start", "stop", "delete", "recover"
	TunnelID  string // Tunnel identifier
	Component string // Component: "ndms", "wg", "backend", "firewall", "process"
	Err       error  // Underlying error
}

// Error returns the error message.
func (e *OpError) Error() string {
	if e.Component != "" {
		return fmt.Sprintf("%s %s [%s]: %v", e.Op, e.TunnelID, e.Component, e.Err)
	}
	return fmt.Sprintf("%s %s: %v", e.Op, e.TunnelID, e.Err)
}

// Unwrap returns the underlying error.
func (e *OpError) Unwrap() error {
	return e.Err
}

// Is checks if the error matches a target error.
func (e *OpError) Is(target error) bool {
	return errors.Is(e.Err, target)
}

// NewOpError creates a new OpError.
func NewOpError(op, tunnelID, component string, err error) *OpError {
	return &OpError{
		Op:        op,
		TunnelID:  tunnelID,
		Component: component,
		Err:       err,
	}
}

// WrapOpError wraps an error with operation context if it's not already an OpError.
func WrapOpError(op, tunnelID string, err error) error {
	if err == nil {
		return nil
	}
	var opErr *OpError
	if errors.As(err, &opErr) {
		return err // Already wrapped
	}
	return &OpError{Op: op, TunnelID: tunnelID, Err: err}
}
