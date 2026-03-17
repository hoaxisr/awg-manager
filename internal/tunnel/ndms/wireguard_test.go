package ndms

import "testing"

func TestFormatHandshakeSecondsAgo(t *testing.T) {
	tests := []struct {
		name string
		ts   int64
		want string
	}{
		{"zero", 0, ""},
		{"negative", -1, ""},
		{"max_int32", 2147483647, ""},
		{"valid_seconds_ago", 60, "non-empty"},
	}
	for _, tt := range tests {
		got := FormatHandshakeSecondsAgo(tt.ts)
		if tt.want == "" && got != "" {
			t.Errorf("%s: got %q, want empty", tt.name, got)
		}
		if tt.want != "" && got == "" {
			t.Errorf("%s: got empty, want non-empty", tt.name)
		}
	}
}
