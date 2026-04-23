// Package semver provides minimal semantic-version comparison for
// dotted-numeric version strings like "2.3.10" or "1.4".
//
// Used by both the updater (awg-manager releases) and the kernel-module
// manager (amneziawg kmod versions) — they need the same comparator but
// historically carried private copies. Keep this package narrow: just a
// Compare function, no parsing/structs, no pre-release/build suffixes.
package semver

import (
	"strconv"
	"strings"
)

// Compare compares two dotted-numeric version strings.
// Returns -1 if a < b, 0 if a == b, 1 if a > b. Missing components are
// treated as 0, so "1.2" == "1.2.0". Non-numeric components also parse
// as 0 — callers that need stricter semantics should validate upstream.
func Compare(a, b string) int {
	partsA := strings.Split(a, ".")
	partsB := strings.Split(b, ".")
	maxLen := len(partsA)
	if len(partsB) > maxLen {
		maxLen = len(partsB)
	}
	for i := 0; i < maxLen; i++ {
		var numA, numB int
		if i < len(partsA) {
			numA, _ = strconv.Atoi(partsA[i])
		}
		if i < len(partsB) {
			numB, _ = strconv.Atoi(partsB[i])
		}
		if numA < numB {
			return -1
		}
		if numA > numB {
			return 1
		}
	}
	return 0
}
