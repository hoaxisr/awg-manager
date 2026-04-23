package storage

// appendUnique returns s with v appended if it was not already present.
// The second return is true when v was added (slice grew), false when
// the slice already contained it (callers treat this as a no-op so they
// can skip writing to disk).
func appendUnique(s []string, v string) ([]string, bool) {
	for _, x := range s {
		if x == v {
			return s, false
		}
	}
	return append(s, v), true
}

// filterOut returns a new slice with every occurrence of v removed.
// Always allocates a new backing array; callers that need to skip the
// write on no-op should check the length themselves.
func filterOut(s []string, v string) []string {
	out := make([]string, 0, len(s))
	for _, x := range s {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

// contains reports whether v appears in s.
func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
