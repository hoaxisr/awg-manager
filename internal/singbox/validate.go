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
