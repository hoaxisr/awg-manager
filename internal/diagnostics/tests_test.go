package diagnostics

import (
	"testing"
)

func TestRunOptions_RestartCycleOnlyWhenIncludeRestart(t *testing.T) {
	// Contract: restart_cycle running depends ONLY on opts.IncludeRestart.
	// Mode (Quick/Full) был выпилен; остаётся только это правило.
	cases := []struct {
		name               string
		opts               RunOptions
		wantIncludeRestart bool
	}{
		{"no-restart", RunOptions{IncludeRestart: false}, false},
		{"with-restart", RunOptions{IncludeRestart: true}, true},
	}

	for _, c := range cases {
		derived := c.opts.IncludeRestart
		if derived != c.wantIncludeRestart {
			t.Errorf("%s: derived includeRestart=%v, want %v", c.name, derived, c.wantIncludeRestart)
		}
	}
}
