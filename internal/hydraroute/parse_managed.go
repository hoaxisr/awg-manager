package hydraroute

import (
	"strings"
)

// HR Neo's oversized-geoip service section: `##impossible to use` followed by
// `#/Too-big-geoip-tag` (cidrfile_migrate_oversized in HR Neo's geodat.c).
const (
	oversizedSectionName   = "impossible to use"
	oversizedSectionTarget = "Too-big-geoip-tag"
)

// isOversizedSection reports whether a block named listName with the `#/`
// target line target is HR Neo's service section. Both must match: a user
// group may not take the name (validateGroup), but files edited by hand could
// still hold a disabled rule called that way.
func isOversizedSection(listName, target string) bool {
	return strings.EqualFold(strings.TrimSpace(listName), oversizedSectionName) &&
		strings.EqualFold(strings.TrimSpace(target), oversizedSectionTarget)
}

// removeOversizedTag drops tag (already in normalizeOversizedTag form) from
// every HR Neo service section in an ip.list body. Sections are recognised
// the way parseCIDRHalves does it: a `##` header and a `#/` target line matching
// isOversizedSection; a block ends at a blank line or the next `##` header.
// Only the `geoip:` lines of those sections that parseCIDRHalves reports are
// considered, and every duplicate of the tag is removed. A section left
// without `geoip:` lines is dropped as a whole, together with its blank-line
// terminator when it would otherwise leave two blank lines in a row (or one
// at the start of the file).
//
// Everything else is kept byte-for-byte, including `\r\n` line endings. The
// result is false (and content is returned unchanged) when the tag is not
// in any service section.
func removeOversizedTag(content, tag string) (string, bool) {
	lines := strings.SplitAfter(content, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1] // artefact of the trailing newline, not a line
	}
	text := func(i int) string { return strings.TrimRight(lines[i], "\r\n") }

	drop := make([]bool, len(lines))
	found := false

	// State of the current block, reset when it ends.
	start := -1      // header index, -1 outside a block
	name := ""       // header name
	service := false // the `#/` target line matched
	removed := 0     // geoip lines dropped from this block
	kept := 0        // geoip lines left in this block

	prevKeptBlank := func(i int) bool {
		for j := i - 1; j >= 0; j-- {
			if !drop[j] {
				return text(j) == ""
			}
		}
		return true // start of file
	}
	// finish closes the block that ends right before line end (the
	// terminator: a blank line, a `##` header or len(lines) for EOF).
	finish := func(end int) {
		if start >= 0 && service && removed > 0 && kept == 0 {
			for j := start; j < end; j++ {
				drop[j] = true
			}
			if end < len(lines) && text(end) == "" && prevKeptBlank(start) {
				drop[end] = true
			}
		}
		start, name, service, removed, kept = -1, "", false, 0, 0
	}

	for i := range lines {
		line := text(i)

		if strings.HasPrefix(line, "##") {
			finish(i)
			start = i
			name = strings.TrimPrefix(line, "##")
			continue
		}
		if line == "" {
			finish(i)
			continue
		}
		if start < 0 {
			continue
		}
		if strings.HasPrefix(line, "#/") {
			if isOversizedSection(name, strings.TrimPrefix(line, "#/")) {
				service = true
			}
			continue
		}
		if !service {
			continue
		}
		// parseCIDRHalves strips a leading `#` before looking for `geoip:`.
		t := strings.TrimPrefix(line, "#")
		if !strings.HasPrefix(t, "geoip:") {
			continue
		}
		if normalizeOversizedTag(t) == tag {
			drop[i] = true
			removed++
			found = true
		} else {
			kept++
		}
	}
	finish(len(lines))

	if !found {
		return content, false
	}
	var sb strings.Builder
	sb.Grow(len(content))
	for i, l := range lines {
		if !drop[i] {
			sb.WriteString(l)
		}
	}
	return sb.String(), true
}

// splitNonEmpty splits s by sep, trims entries, drops empties.
func splitNonEmpty(s, sep string) []string {
	parts := strings.Split(s, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
