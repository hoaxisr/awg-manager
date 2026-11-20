package uplink

import "testing"

func TestPathSelectionFailsOverAndReturns(t *testing.T) {
	var selection PathSelection
	active := 0
	for _, round := range []struct {
		direct, tunnel PathProbeResult
		want           int
	}{
		{PathUnreachable, PathReachable, 0},
		{PathUnreachable, PathReachable, 1},
		{PathReachable, PathReachable, 1},
		{PathUnchecked, PathReachable, 1},
		{PathReachable, PathReachable, 1},
		{PathReachable, PathReachable, 0},
	} {
		if got := selection.Observe(active, []PathProbeResult{round.direct, round.tunnel}, 2, 2); got != round.want {
			t.Fatalf("observed %+v from %d: got %d", round, active, got)
		}
		active = round.want
	}
}

func TestPathSelectionRequiresReachableAlternative(t *testing.T) {
	var selection PathSelection
	if got := selection.Observe(0, []PathProbeResult{PathUnreachable, PathUnreachable, PathUnchecked}, 1, 1); got != 0 {
		t.Fatalf("lost active route during common failure: %d", got)
	}
	if got := selection.Observe(0, []PathProbeResult{PathUnreachable, PathReachable, PathReachable}, 1, 1); got != 1 {
		t.Fatalf("did not select first confirmed reserve: %d", got)
	}
	if got := selection.Observe(-1, []PathProbeResult{PathUnchecked, PathUnreachable, PathUnchecked}, 1, 1); got != -1 {
		t.Fatalf("selected unconfirmed route at startup: %d", got)
	}
}
