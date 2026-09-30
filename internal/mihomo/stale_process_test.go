package mihomo

import "testing"

func TestIsManagedDaemonCmdlineMatchesOnlyExactOperatorCommand(t *testing.T) {
	binaryPath := "/opt/etc/awg-manager/mihomo/mihomo"
	configDir := "/opt/etc/awg-manager/mihomo"

	tests := []struct {
		name    string
		cmdline string
		want    bool
	}{
		{
			name:    "managed daemon",
			cmdline: binaryPath + "\x00-d\x00" + configDir + "\x00",
			want:    true,
		},
		{
			name:    "config validation is not daemon",
			cmdline: binaryPath + "\x00-d\x00" + configDir + "\x00-t\x00",
		},
		{
			name:    "another binary is not managed",
			cmdline: "/opt/bin/mihomo\x00-d\x00" + configDir + "\x00",
		},
		{
			name:    "another config is not managed",
			cmdline: binaryPath + "\x00-d\x00/opt/etc/other-mihomo\x00",
		},
		{
			name:    "similar path is not managed",
			cmdline: binaryPath + "-other\x00-d\x00" + configDir + "\x00",
		},
		{
			name:    "missing nul terminator still matches proc payload",
			cmdline: binaryPath + "\x00-d\x00" + configDir,
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isManagedDaemonCmdline([]byte(tt.cmdline), binaryPath, configDir); got != tt.want {
				t.Fatalf("isManagedDaemonCmdline() = %v, want %v", got, tt.want)
			}
		})
	}
}
