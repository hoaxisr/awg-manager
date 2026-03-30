package pingcheck

import "testing"

func TestGetTunnelPingStatus_NoMonitor(t *testing.T) {
	s := &Service{monitors: make(map[string]*tunnelMonitor)}
	info := s.GetTunnelPingStatus("awg0")
	if info.Status != "disabled" {
		t.Errorf("got %q, want disabled", info.Status)
	}
	if info.RestartCount != 0 || info.FailCount != 0 || info.FailThreshold != 0 {
		t.Error("expected all zero values for missing monitor")
	}
}

func TestGetTunnelPingStatus_Alive(t *testing.T) {
	s := &Service{monitors: map[string]*tunnelMonitor{
		"awg0": {
			failCount:     1,
			restartCount:  0,
			failThreshold: 3,
			lastResult:    &CheckResult{Success: true},
		},
	}}
	info := s.GetTunnelPingStatus("awg0")
	if info.Status != "alive" {
		t.Errorf("got %q, want alive", info.Status)
	}
	if info.FailCount != 1 {
		t.Errorf("failCount: got %d, want 1", info.FailCount)
	}
	if info.FailThreshold != 3 {
		t.Errorf("failThreshold: got %d, want 3", info.FailThreshold)
	}
}

func TestGetTunnelPingStatus_Recovering_NoResult(t *testing.T) {
	s := &Service{monitors: map[string]*tunnelMonitor{
		"awg0": {
			failCount:     0,
			restartCount:  2,
			failThreshold: 3,
			lastResult:    nil,
		},
	}}
	info := s.GetTunnelPingStatus("awg0")
	if info.Status != "recovering" {
		t.Errorf("got %q, want recovering", info.Status)
	}
	if info.RestartCount != 2 {
		t.Errorf("restartCount: got %d, want 2", info.RestartCount)
	}
}

func TestGetTunnelPingStatus_Recovering_LastFailed(t *testing.T) {
	s := &Service{monitors: map[string]*tunnelMonitor{
		"awg0": {
			restartCount:  1,
			failThreshold: 3,
			lastResult:    &CheckResult{Success: false},
		},
	}}
	info := s.GetTunnelPingStatus("awg0")
	if info.Status != "recovering" {
		t.Errorf("got %q, want recovering", info.Status)
	}
}

func TestGetTunnelPingStatus_BackToAlive(t *testing.T) {
	s := &Service{monitors: map[string]*tunnelMonitor{
		"awg0": {
			restartCount:  3,
			failThreshold: 3,
			lastResult:    &CheckResult{Success: true},
		},
	}}
	info := s.GetTunnelPingStatus("awg0")
	if info.Status != "alive" {
		t.Errorf("got %q, want alive (recovered)", info.Status)
	}
}
