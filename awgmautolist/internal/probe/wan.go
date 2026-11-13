package probe

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/model"
)

// CheckWAN proves that one path can complete an HTTP transaction at all.
// Any nonzero HTTP status is sufficient, including an error status: this is a
// liveness check, not a content check. No --interface means the default route.
func CheckWAN(ctx context.Context, url string, path Path) Result {
	started := time.Now()
	if strings.TrimSpace(url) == "" {
		return Result{Verdict: model.VerdictError, Duration: time.Since(started), Detail: "WAN check requires a URL"}
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	args := []string{
		"--silent", "--show-error", "--noproxy", "*",
		"--output", "/dev/null", "--write-out", "%{http_code}",
		"--connect-timeout", formatSeconds(probeTimeout),
		"--max-time", formatSeconds(probeTimeout),
		"--user-agent", userAgent,
	}
	if path.Interface != "" {
		args = append(args, "--interface", path.Interface)
	}
	args = append(args, url)

	cmd := exec.CommandContext(ctx, "curl", args...)
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	out, err := cmd.Output()

	result := Result{Duration: time.Since(started)}
	detail := strings.TrimSpace(stderr.String())
	if detail == "" && err != nil {
		detail = err.Error()
	}
	result.Detail = detail

	code, parseErr := strconv.Atoi(strings.TrimSpace(string(out)))
	if err == nil && parseErr == nil && code > 0 {
		result.Verdict = model.VerdictReachable
		result.HTTPStatus = code
		result.Detail = "HTTP " + strconv.Itoa(code)
		return result
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.Verdict = model.VerdictBlocked
		return result
	}
	result.Verdict = model.VerdictError
	return result
}
