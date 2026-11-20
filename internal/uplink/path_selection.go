package uplink

// PathProbeResult describes one independent observation of a candidate.
type PathProbeResult uint8

const (
	PathUnchecked PathProbeResult = iota
	PathReachable
	PathUnreachable
)

type pathStreak struct {
	result PathProbeResult
	rounds int
}

// PathSelection tracks observations for one revision of an ordered group.
// Use a new value whenever the candidate order or probe target changes.
// The caller serializes observations and supplies the confirmed active index.
type PathSelection struct {
	streaks []pathStreak
}

// Observe returns the preferred active index. An unchecked path does not
// contribute to either threshold. If all candidates fail, keep the active
// candidate. An active index of -1 means no route has been confirmed yet.
func (s *PathSelection) Observe(active int, results []PathProbeResult, failures, recoveries int) int {
	if len(s.streaks) != len(results) {
		s.streaks = make([]pathStreak, len(results))
	}
	failures = max(1, failures)
	recoveries = max(1, recoveries)
	limit := max(failures, recoveries)
	for i, result := range results {
		streak := &s.streaks[i]
		if result != PathReachable && result != PathUnreachable {
			*streak = pathStreak{}
		} else if streak.result != result {
			*streak = pathStreak{result: result, rounds: 1}
		} else if streak.rounds < limit {
			streak.rounds++
		}
	}
	if active < 0 || active >= len(results) {
		active = -1
	}
	activeFailed := active == -1 || (s.streaks[active].result == PathUnreachable && s.streaks[active].rounds >= failures)
	for i, streak := range s.streaks {
		if streak.result != PathReachable {
			continue
		}
		if activeFailed || (i < active && streak.rounds >= recoveries) {
			return i
		}
	}
	return active
}
