// internal/singbox/validate.go
package singbox

import (
	"fmt"
	"os/exec"
)

type Validator struct {
	binary string
	exec   func(bin string, args ...string) ([]byte, error)
}

// DefaultBinaryPath returns the canonical on-disk path for the managed
// sing-box binary (mirrors installer.DefaultBinaryPath via the package-
// level defaultBinary const so validator callers don't need to import the
// installer sub-package).
func DefaultBinaryPath() string { return defaultBinary }

func NewValidator(binary string) *Validator {
	return &Validator{
		binary: binary,
		exec: func(bin string, args ...string) ([]byte, error) {
			return exec.Command(bin, args...).CombinedOutput()
		},
	}
}

func (v *Validator) Validate(configDir string) error {
	out, err := v.exec(v.binary, "check", "-C", configDir)
	if err != nil {
		return fmt.Errorf("sing-box check failed: %s: %w", string(out), err)
	}
	return nil
}
