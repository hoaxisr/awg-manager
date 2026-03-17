package tunnel

import (
	"errors"
	"testing"
)

func TestOpError_Error(t *testing.T) {
	tests := []struct {
		name     string
		err      *OpError
		expected string
	}{
		{
			name: "with component",
			err: &OpError{
				Op:        "start",
				TunnelID:  "awg0",
				Component: "ndms",
				Err:       errors.New("connection refused"),
			},
			expected: "start awg0 [ndms]: connection refused",
		},
		{
			name: "without component",
			err: &OpError{
				Op:       "stop",
				TunnelID: "awg1",
				Err:      errors.New("not running"),
			},
			expected: "stop awg1: not running",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.expected {
				t.Errorf("OpError.Error() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestOpError_Unwrap(t *testing.T) {
	underlying := errors.New("underlying error")
	opErr := &OpError{
		Op:       "start",
		TunnelID: "awg0",
		Err:      underlying,
	}

	if got := opErr.Unwrap(); got != underlying {
		t.Errorf("OpError.Unwrap() = %v, want %v", got, underlying)
	}
}

func TestOpError_Is(t *testing.T) {
	opErr := &OpError{
		Op:       "start",
		TunnelID: "awg0",
		Err:      ErrAlreadyRunning,
	}

	if !opErr.Is(ErrAlreadyRunning) {
		t.Error("OpError.Is(ErrAlreadyRunning) = false, want true")
	}

	if opErr.Is(ErrNotRunning) {
		t.Error("OpError.Is(ErrNotRunning) = true, want false")
	}
}

func TestOpError_ErrorsIs(t *testing.T) {
	opErr := &OpError{
		Op:       "start",
		TunnelID: "awg0",
		Err:      ErrAlreadyRunning,
	}

	// Test using errors.Is
	if !errors.Is(opErr, ErrAlreadyRunning) {
		t.Error("errors.Is(opErr, ErrAlreadyRunning) = false, want true")
	}
}

func TestNewOpError(t *testing.T) {
	err := NewOpError("create", "awg0", "ndms", errors.New("failed"))

	if err.Op != "create" {
		t.Errorf("Op = %q, want %q", err.Op, "create")
	}
	if err.TunnelID != "awg0" {
		t.Errorf("TunnelID = %q, want %q", err.TunnelID, "awg0")
	}
	if err.Component != "ndms" {
		t.Errorf("Component = %q, want %q", err.Component, "ndms")
	}
}

func TestSentinelErrors(t *testing.T) {
	// Ensure all sentinel errors are unique
	sentinels := []error{
		ErrNotFound,
		ErrAlreadyExists,
		ErrAlreadyRunning,
		ErrNotRunning,
		ErrTransitioning,
		ErrAddressInUse,
	}

	for i, err1 := range sentinels {
		for j, err2 := range sentinels {
			if i != j && errors.Is(err1, err2) {
				t.Errorf("Sentinel errors %v and %v should not match", err1, err2)
			}
		}
	}
}
