package hydraroute

import "testing"

func TestDetect_NotInstalled(t *testing.T) {
	s := Detect()
	if s.Installed {
		t.Skip("HydraRoute is installed on this machine, skipping negative test")
	}
	if s.Running {
		t.Error("Running should be false when not installed")
	}
}
