package aiassistant

import (
	"testing"
)

func TestValidateDiagnosticCommand_Safe(t *testing.T) {
	safeCommands := []string{
		"ip addr show",
		"ip route show table all",
		"ip rule show",
		"curl -4 -v -m 5 --interface opkgtun10 http://cp.cloudflare.com/generate_204",
		"curl -s http://127.0.0.1:79/rci/show/interface",
		"ping -c 3 -W 2 -I opkgtun10 1.1.1.1",
		"traceroute -i opkgtun10 -m 10 1.1.1.1",
		"nslookup google.com 1.1.1.1",
		"iptables -t mangle -nvL",
		"iptables -nvL PREROUTING -t nat",
		"iptables -S",
		"nft list ruleset",
		"dmesg | tail -n 30",
		"logread | grep -i error",
		"cat /proc/net/dev",
		"ps w",
		"top -b -n 1",
		"free -m",
		"df -h",
		"ss -tlpn",
		"netstat -tlpn",
		"awg show",
		"wg show",
		"conntrack -L",
		"/opt/etc/init.d/S51sing-box status",
		"opkg list-installed",
		"opkg status sing-box",
	}

	for _, cmd := range safeCommands {
		if err := ValidateDiagnosticCommand(cmd); err != nil {
			t.Errorf("Expected command %q to be valid, got error: %v", cmd, err)
		}
	}
}

func TestValidateDiagnosticCommand_Blocked(t *testing.T) {
	unsafeCommands := []string{
		"rm -rf /tmp/*",
		"reboot",
		"halt",
		"poweroff",
		"shutdown -r now",
		"echo test > /tmp/bad.txt",
		"cat foo >> /tmp/bad.txt",
		"iptables -t mangle -F",
		"iptables -A PREROUTING -j DROP",
		"iptables -D PREROUTING 1",
		"nft add table inet my_table",
		"nft flush ruleset",
		"opkg install htop",
		"opkg remove sing-box",
		"kill -9 1234",
		"pkill sing-box",
		"dd if=/dev/zero of=/dev/sda",
		"sed -i 's/foo/bar/g' /opt/etc/config",
		"cat /etc/passwd | tee /tmp/stolen.txt",
	}

	for _, cmd := range unsafeCommands {
		if err := ValidateDiagnosticCommand(cmd); err == nil {
			t.Errorf("Expected command %q to be blocked, but it passed!", cmd)
		}
	}
}

func TestSanitizeDiagnosticOutput(t *testing.T) {
	raw := `interface: opkgtun10
  public key: PzSHLZ3gEd/PADKJ/w5gKxtaGiz9aDvMzX1v6mW8UXI=
  private key: t5dWByAPRLA3y8i5w9zOT50fGqBKT2KSDHj059RYJpM=
  peer: H53zmK8yFnSj9nLD03ZwoKgnsszvQnPrfWLz0yNdgxY=
  preshared key: 4MVbAES9UWU8PndZpbO+pa7D7sXpcYgzrnntgYKMrlY=
  endpoint: 78.17.149.168:2711`

	sanitized := SanitizeDiagnosticOutput(raw, "", 1000)
	if sanitized == "" {
		t.Fatal("Expected non-empty output")
	}
	if stringContains(sanitized, "t5dWByAPRLA3y8i5w9zOT50fGqBKT2KSDHj059RYJpM=") {
		t.Errorf("Private key was not masked: %s", sanitized)
	}
	if stringContains(sanitized, "4MVbAES9UWU8PndZpbO+pa7D7sXpcYgzrnntgYKMrlY=") {
		t.Errorf("Preshared key was not masked: %s", sanitized)
	}
}

func stringContains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 || (len(s) > 0 && len(substr) > 0 && findSubstr(s, substr)))
}

func findSubstr(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
