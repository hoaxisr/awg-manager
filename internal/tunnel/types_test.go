package tunnel

import (
	"testing"
)

func TestState_String(t *testing.T) {
	tests := []struct {
		state    State
		expected string
	}{
		{StateUnknown, "unknown"},
		{StateNotCreated, "not_created"},
		{StateStopped, "stopped"},
		{StateStarting, "starting"},
		{StateRunning, "running"},
		{StateStopping, "stopping"},
		{StateBroken, "broken"},
		{State(99), "state(99)"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := tt.state.String(); got != tt.expected {
				t.Errorf("State.String() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestState_IsTerminal(t *testing.T) {
	tests := []struct {
		state    State
		terminal bool
	}{
		{StateUnknown, false},
		{StateNotCreated, true},
		{StateStopped, true},
		{StateStarting, false},
		{StateRunning, true},
		{StateStopping, false},
		{StateBroken, true},
	}

	for _, tt := range tests {
		t.Run(tt.state.String(), func(t *testing.T) {
			if got := tt.state.IsTerminal(); got != tt.terminal {
				t.Errorf("State.IsTerminal() = %v, want %v", got, tt.terminal)
			}
		})
	}
}

func TestNewNames(t *testing.T) {
	tests := []struct {
		tunnelID     string
		wantNum      string
		wantNDMS     string
		wantIface    string
		wantConf     string
		wantPID      string
		wantSocket   string
	}{
		{
			tunnelID:   "awg0",
			wantNum:    "0",
			wantNDMS:   "OpkgTun0",
			wantIface:  "opkgtun0",
			wantConf:   "/opt/etc/awg-manager/awg0.conf",
			wantPID:    "/opt/var/run/awg-manager/awg0.pid",
			wantSocket: "/tmp/run/amneziawg/opkgtun0.sock",
		},
		{
			tunnelID:   "awg10",
			wantNum:    "10",
			wantNDMS:   "OpkgTun10",
			wantIface:  "opkgtun10",
			wantConf:   "/opt/etc/awg-manager/awg10.conf",
			wantPID:    "/opt/var/run/awg-manager/awg10.pid",
			wantSocket: "/tmp/run/amneziawg/opkgtun10.sock",
		},
		{
			tunnelID:   "tunnel5",
			wantNum:    "5",
			wantNDMS:   "OpkgTun5",
			wantIface:  "opkgtun5",
			wantConf:   "/opt/etc/awg-manager/tunnel5.conf",
			wantPID:    "/opt/var/run/awg-manager/tunnel5.pid",
			wantSocket: "/tmp/run/amneziawg/opkgtun5.sock",
		},
		{
			tunnelID:   "nodigits",
			wantNum:    "0",
			wantNDMS:   "OpkgTun0",
			wantIface:  "opkgtun0",
			wantConf:   "/opt/etc/awg-manager/nodigits.conf",
			wantPID:    "/opt/var/run/awg-manager/nodigits.pid",
			wantSocket: "/tmp/run/amneziawg/opkgtun0.sock",
		},
	}

	for _, tt := range tests {
		t.Run(tt.tunnelID, func(t *testing.T) {
			names := NewNames(tt.tunnelID)

			if names.TunnelID != tt.tunnelID {
				t.Errorf("TunnelID = %v, want %v", names.TunnelID, tt.tunnelID)
			}
			if names.TunnelNum != tt.wantNum {
				t.Errorf("TunnelNum = %v, want %v", names.TunnelNum, tt.wantNum)
			}
			if names.NDMSName != tt.wantNDMS {
				t.Errorf("NDMSName = %v, want %v", names.NDMSName, tt.wantNDMS)
			}
			if names.IfaceName != tt.wantIface {
				t.Errorf("IfaceName = %v, want %v", names.IfaceName, tt.wantIface)
			}
			if names.ConfPath != tt.wantConf {
				t.Errorf("ConfPath = %v, want %v", names.ConfPath, tt.wantConf)
			}
			if names.PIDPath != tt.wantPID {
				t.Errorf("PIDPath = %v, want %v", names.PIDPath, tt.wantPID)
			}
			if names.SocketPath != tt.wantSocket {
				t.Errorf("SocketPath = %v, want %v", names.SocketPath, tt.wantSocket)
			}
		})
	}
}

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid config",
			cfg: Config{
				ID:       "awg0",
				Address:  "10.0.0.1",
				MTU:      1420,
				ConfPath: "/opt/etc/awg-manager/awg0.conf",
			},
			wantErr: false,
		},
		{
			name: "missing ID",
			cfg: Config{
				Address:  "10.0.0.1",
				ConfPath: "/opt/etc/awg-manager/awg0.conf",
			},
			wantErr: true,
			errMsg:  "tunnel ID is required",
		},
		{
			name: "missing address",
			cfg: Config{
				ID:       "awg0",
				ConfPath: "/opt/etc/awg-manager/awg0.conf",
			},
			wantErr: true,
			errMsg:  "address is required",
		},
		{
			name: "missing config path",
			cfg: Config{
				ID:      "awg0",
				Address: "10.0.0.1",
			},
			wantErr: true,
			errMsg:  "config path is required",
		},
		{
			name: "zero MTU gets default",
			cfg: Config{
				ID:       "awg0",
				Address:  "10.0.0.1",
				MTU:      0,
				ConfPath: "/opt/etc/awg-manager/awg0.conf",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr {
				if err == nil {
					t.Errorf("Config.Validate() error = nil, want error containing %q", tt.errMsg)
				} else if tt.errMsg != "" && err.Error() != tt.errMsg {
					t.Errorf("Config.Validate() error = %q, want %q", err.Error(), tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("Config.Validate() unexpected error = %v", err)
				}
			}

			// Check MTU default is applied
			if !tt.wantErr && tt.cfg.MTU == 0 {
				// Validate should set default
				if tt.cfg.MTU != 1420 {
					t.Errorf("Config.Validate() did not set default MTU, got %d", tt.cfg.MTU)
				}
			}
		})
	}
}

func TestExtractTunnelNum(t *testing.T) {
	tests := []struct {
		id   string
		want string
	}{
		{"awg0", "0"},
		{"awg1", "1"},
		{"awg123", "123"},
		{"tunnel5", "5"},
		{"test99abc", "99abc"},
		{"nodigits", "0"},
		{"", "0"},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			if got := extractTunnelNum(tt.id); got != tt.want {
				t.Errorf("extractTunnelNum(%q) = %q, want %q", tt.id, got, tt.want)
			}
		})
	}
}
