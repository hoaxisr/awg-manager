package httpapi

import "testing"

func TestValidateLoopbackListen(t *testing.T) {
	for _, valid := range []string{"127.0.0.1:2226", "[::1]:2226", "localhost:2226"} {
		if err := ValidateLoopbackListen(valid); err != nil {
			t.Errorf("%s: %v", valid, err)
		}
	}
	for _, invalid := range []string{"0.0.0.0:2226", "192.168.1.1:2226", "example.com:2226", "bad"} {
		if err := ValidateLoopbackListen(invalid); err == nil {
			t.Errorf("accepted %s", invalid)
		}
	}
}
