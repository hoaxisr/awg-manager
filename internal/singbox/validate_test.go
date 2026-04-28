// internal/singbox/validate_test.go
package singbox

import (
	"errors"
	"testing"
)

func TestValidator_Success(t *testing.T) {
	v := &Validator{
		binary: "echo",
		exec: func(bin string, args ...string) ([]byte, error) {
			if bin != "echo" {
				t.Errorf("binary: %s", bin)
			}
			if len(args) != 3 || args[0] != "check" || args[1] != "-C" || args[2] != "/tmp/config.d" {
				t.Errorf("args: %v", args)
			}
			return nil, nil
		},
	}
	if err := v.Validate("/tmp/config.d"); err != nil {
		t.Fatal(err)
	}
}

func TestValidator_Failure(t *testing.T) {
	v := &Validator{
		binary: "sing-box",
		exec: func(bin string, args ...string) ([]byte, error) {
			return []byte("config error: invalid outbound"), errors.New("exit 1")
		},
	}
	err := v.Validate("/tmp/config.d")
	if err == nil {
		t.Fatal("expected error")
	}
}
