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

// Run executes iptables and surfaces its stderr on failure. Голый err даёт
// только «exit status 1», по которому нельзя отличить занятый xtables-лок от
// отсутствующего модуля или отвергнутой ядром спеки — тот же довод, что уже
// записан у RestoreNoflush ниже.
func Run(ctx context.Context, args ...string) error {
	full := append([]string{"-w"}, args...)
	res, err := exec.Run(ctx, Binary, full...)
	if err != nil {
		return exec.FormatError(res, err)
	}
	return nil
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
	if err != nil {
		return res.Stdout, exec.FormatError(res, err)
	}
	return res.Stdout, nil
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
