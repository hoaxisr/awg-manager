package iptables

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/sys/exec"
)

const (
	Binary          = "/opt/sbin/iptables"
	RestoreBinary   = "/opt/sbin/iptables-restore"
	MaxRestoreTries = 3
	RetryBaseWait   = time.Second
)

func Run(ctx context.Context, args ...string) error {
	full := append([]string{"-w"}, args...)
	_, err := exec.Run(ctx, Binary, full...)
	return err
}

// RunOutput runs iptables and returns its stdout. Used by read-only queries
// (e.g. `-S PREROUTING`) where the caller needs to inspect the rule listing,
// not just the exit code.
func RunOutput(ctx context.Context, args ...string) (string, error) {
	full := append([]string{"-w"}, args...)
	res, err := exec.Run(ctx, Binary, full...)
	if res == nil {
		return "", err
	}
	return res.Stdout, err
}

func RestoreNoflush(ctx context.Context, input string) error {
	var lastErr error
	var lastResult *exec.Result
	for attempt := 0; attempt < MaxRestoreTries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * RetryBaseWait)
		}
		result, err := exec.RunWithOptions(ctx, RestoreBinary, []string{"--noflush"}, exec.Options{
			Stdin: strings.NewReader(input),
		})
		if err == nil {
			return nil
		}
		lastErr = err
		lastResult = result
	}
	// Surface stderr (e.g. `iptables-restore: line N failed`) so the
	// caller's log entry actually tells us where the kernel rejected the
	// batch. Without FormatError, we get only "exit status 1" which is
	// useless for diagnosing parse vs. commit failures.
	return fmt.Errorf("iptables-restore --noflush: %w", exec.FormatError(lastResult, lastErr))
}
