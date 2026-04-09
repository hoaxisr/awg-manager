// Package exec provides command execution with timeout support.
package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

var (
	// ErrTimeout indicates command exceeded timeout.
	ErrTimeout = errors.New("command timed out")

	// DefaultTimeout for commands.
	DefaultTimeout = 30 * time.Second
)

// Result holds command execution result.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Options for command execution.
type Options struct {
	Timeout time.Duration
	Env     []string
	Dir     string
	Stdin   io.Reader
}

// Run executes command with default timeout.
func Run(ctx context.Context, name string, args ...string) (*Result, error) {
	return RunWithOptions(ctx, name, args, Options{})
}

// RunWithOptions executes command with custom options.
func RunWithOptions(ctx context.Context, name string, args []string, opts Options) (*Result, error) {
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)

	// Create new process group for proper cleanup (platform-specific).
	setCommandProcessGroup(cmd)

	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}
	if len(opts.Env) > 0 {
		cmd.Env = opts.Env
	} else {
		// Defence-in-depth: scrub dynamic linker variables from inherited env.
		// On Keenetic + Entware, a stale LD_LIBRARY_PATH=/lib:/usr/lib: (e.g. from
		// an opkg postinst-spawned restart, or a misguided init script) makes ld.so
		// load Keenetic system libraries (libssl, libcrypto) instead of Entware ones,
		// causing SIGSEGV/SIGBUS in curl and other Entware binaries. We never need
		// these vars to be inherited — Entware binaries find their libs via the
		// embedded /opt/lib/ld-linux-*.so interpreter and DT_RUNPATH.
		cmd.Env = scrubLinkerEnv(os.Environ())
	}
	if opts.Stdin != nil {
		cmd.Stdin = opts.Stdin
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	result := &Result{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}

	if ctx.Err() == context.DeadlineExceeded {
		// Kill the process group to clean up any children (platform-specific).
		killCommandProcessGroup(cmd)
		return result, ErrTimeout
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		}
		return result, err
	}

	return result, nil
}

// FormatError enriches an error with stderr and exit code from the command result.
// Returns nil if err is nil.
func FormatError(result *Result, err error) error {
	if err == nil {
		return nil
	}
	if result == nil {
		return err
	}
	stderr := strings.TrimSpace(result.Stderr)
	if stderr != "" {
		return fmt.Errorf("%w (exit %d, stderr: %s)", err, result.ExitCode, stderr)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("%w (exit %d)", err, result.ExitCode)
	}
	return err
}

// Shell executes command in shell (sh -c).
func Shell(ctx context.Context, command string) (*Result, error) {
	return Run(ctx, "sh", "-c", command)
}

// scrubLinkerEnv returns a copy of env with dynamic-linker variables removed.
// See the comment in RunWithOptions for the rationale.
func scrubLinkerEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		if strings.HasPrefix(e, "LD_LIBRARY_PATH=") || strings.HasPrefix(e, "LD_PRELOAD=") {
			continue
		}
		out = append(out, e)
	}
	return out
}
