package wdtt

import "testing"

func TestAppendVkAuthArgs(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", []string{"-vk-auth-mode", "vkcalls"}},
		{"vkcalls", []string{"-vk-auth-mode", "vkcalls"}},
		{"legacy", []string{"-vk-auth-mode", "legacy"}},
		{"anonymous", []string{"-vk-auth-mode", "anonymous"}},
		{"account", []string{"-vk-auth-mode", "account"}},
		{"custom", []string{"-vk-auth-mode", "custom"}},
	}
	for _, tc := range tests {
		var args []string
		appendVkAuthArgs(&args, tc.in)
		if len(args) != len(tc.want) {
			t.Fatalf("appendVkAuthArgs(%q) = %v, want %v", tc.in, args, tc.want)
		}
		for i := range tc.want {
			if args[i] != tc.want[i] {
				t.Fatalf("appendVkAuthArgs(%q)[%d] = %q, want %q", tc.in, i, args[i], tc.want[i])
			}
		}
	}
}

func TestBuildClientArgsRawMode(t *testing.T) {
	args := buildClientArgs(ClientConfig{
		Peer:     "203.0.113.5:56013",
		Password: "secret",
		VKHashes: "abc",
		ConnMode: ConnModeRaw,
	}, "")
	joined := stringsJoinArgs(args)
	for _, need := range []string{"-mode", "rawtun", "-peer", "203.0.113.5:56013"} {
		if !containsArgPair(args, need) {
			t.Fatalf("missing %q in %s", need, joined)
		}
	}
}

func stringsJoinArgs(args []string) string {
	out := ""
	for i, a := range args {
		if i > 0 {
			out += " "
		}
		out += a
	}
	return out
}

func containsArgPair(args []string, flag string) bool {
	for i := 0; i < len(args); i++ {
		if args[i] == flag {
			return true
		}
	}
	return false
}
