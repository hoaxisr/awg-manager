// internal/singbox/validate.go
package singbox

import (
	"fmt"
	"os/exec"
)

// Validator runs `sing-box check -c <abs-path>`.
type Validator struct {
	binary string
	exec   func(bin string, args ...string) ([]byte, error)
}

func NewValidator(binary string) *Validator {
	return &Validator{
		binary: binary,
		exec: func(bin string, args ...string) ([]byte, error) {
			return exec.Command(bin, args...).CombinedOutput()
		},
	}
}

// Validate runs `sing-box check -c <absPath>`. absPath MUST be absolute.
func (v *Validator) Validate(absPath string) error {
	out, err := v.exec(v.binary, "check", "-c", absPath)
	if err != nil {
		return fmt.Errorf("sing-box check failed: %s: %w", string(out), err)
	}
	return nil
}
