// internal/singbox/validate_test.go
package singbox

import (
	"errors"
	"testing"
)

func TestValidator_Success(t *testing.T) {
	v := &Validator{
		binary: "echo", // echo returns 0 always
		exec: func(bin string, args ...string) ([]byte, error) {
			if bin != "echo" {
				t.Errorf("binary: %s", bin)
			}
			if len(args) != 2 || args[0] != "check" || args[1] != "-c" {
				// our impl always passes absolute path as last arg
				if len(args) < 3 || args[0] != "check" || args[1] != "-c" {
					t.Errorf("args: %v", args)
				}
			}
			return nil, nil
		},
	}
	if err := v.Validate("/tmp/config.json"); err != nil {
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
	err := v.Validate("/tmp/config.json")
	if err == nil {
		t.Fatal("expected error")
	}
}
