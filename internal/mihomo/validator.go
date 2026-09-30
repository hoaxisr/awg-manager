package mihomo

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"
)

const (
	defaultValidationTimeout = 60 * time.Second
	maxValidationOutputBytes = 64 * 1024 // 64 KiB
)

// BinaryValidator implements ConfigFileValidator using the real mihomo binary.
type BinaryValidator struct {
	binPath string
}

// NewBinaryValidator creates a validator using the specified mihomo binary path.
func NewBinaryValidator(binPath string) *BinaryValidator {
	return &BinaryValidator{binPath: binPath}
}

// ValidateConfigFile runs `mihomo -d <dir> -f <configPath> -t` to validate configuration syntax and semantics.
func (v *BinaryValidator) ValidateConfigFile(ctx context.Context, configPath string) error {
	if v.binPath == "" {
		return fmt.Errorf("mihomo binary validator: empty binary path")
	}

	valCtx, cancel := context.WithTimeout(ctx, defaultValidationTimeout)
	defer cancel()

	configDir := filepath.Dir(configPath)
	cmd := exec.CommandContext(valCtx, v.binPath, "-d", configDir, "-f", configPath, "-t")

	var outBuf bytes.Buffer
	cmd.Stdout = &cappedWriter{w: &outBuf, limit: maxValidationOutputBytes}
	cmd.Stderr = &cappedWriter{w: &outBuf, limit: maxValidationOutputBytes}

	if err := cmd.Run(); err != nil {
		if valCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("mihomo config validation timed out after %s: %w", defaultValidationTimeout, err)
		}
		return fmt.Errorf("mihomo config validation failed (%s -t): %w\noutput: %s", configPath, err, outBuf.String())
	}

	return nil
}

type cappedWriter struct {
	w     *bytes.Buffer
	limit int
}

func (cw *cappedWriter) Write(p []byte) (n int, err error) {
	remaining := cw.limit - cw.w.Len()
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		n, err = cw.w.Write(p[:remaining])
		return len(p), err
	}
	return cw.w.Write(p)
}
