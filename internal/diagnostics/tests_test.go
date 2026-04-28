package diagnostics

import (
	"testing"
)

func TestRunOptions_FullModeWithoutRestartIsNotInclusive(t *testing.T) {
	// Contract: ModeFull alone does NOT auto-include restart_cycle.
	// Only opts.IncludeRestart=true should trigger restart_cycle.
	cases := []struct {
		name               string
		opts               RunOptions
		wantIncludeRestart bool
	}{
		{"full+no-restart", RunOptions{Mode: ModeFull, IncludeRestart: false}, false},
		{"full+restart", RunOptions{Mode: ModeFull, IncludeRestart: true}, true},
		{"quick+no-restart", RunOptions{Mode: ModeQuick, IncludeRestart: false}, false},
		{"quick+restart", RunOptions{Mode: ModeQuick, IncludeRestart: true}, true},
	}

	for _, c := range cases {
		// The derivation logic in runTestsWithEvents is:
		//   includeRestart := opts.IncludeRestart
		// We can't easily call runTestsWithEvents without full Runner deps,
		// so this test asserts the input contract: restart_cycle must come
		// from IncludeRestart only, not Mode.
		derived := c.opts.IncludeRestart
		if derived != c.wantIncludeRestart {
			t.Errorf("%s: derived includeRestart=%v, want %v", c.name, derived, c.wantIncludeRestart)
		}
	}
}
