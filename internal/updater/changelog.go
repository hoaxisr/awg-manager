package updater

import (
	"regexp"
	"sort"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/sys/semver"
)

// Entry is one version's changelog as parsed from CHANGELOG.md.
type Entry struct {
	Version string  `json:"version"`
	Date    string  `json:"date"`
	Groups  []Group `json:"groups"`
}

// Group is a Keep-a-Changelog section (Added/Fixed/...) within a version.
type Group struct {
	Heading string   `json:"heading"`
	Items   []string `json:"items"`
}

// versionLine matches "## [2.8.0] - 2026-04-17" with optional whitespace.
var versionLine = regexp.MustCompile(`^##\s+\[([^\]]+)\]\s+-\s+(\S+)\s*$`)

// groupLine matches "### Added" (any Keep-a-Changelog heading).
var groupLine = regexp.MustCompile(`^###\s+(.+?)\s*$`)

// itemLine matches "- something" or "* something".
var itemLine = regexp.MustCompile(`^[-*]\s+(.+?)\s*$`)

// ParseChangelog returns version-keyed entries parsed from a CHANGELOG.md body.
// Unparseable leftover lines are skipped silently so a partial file still
// produces usable data.
func ParseChangelog(md string) (map[string]Entry, error) {
	out := make(map[string]Entry)
	var cur *Entry
	var curGroup *Group

	flushGroup := func() {
		if cur != nil && curGroup != nil && len(curGroup.Items) > 0 {
			cur.Groups = append(cur.Groups, *curGroup)
		}
		curGroup = nil
	}
	flushEntry := func() {
		flushGroup()
		if cur != nil {
			out[cur.Version] = *cur
			cur = nil
		}
	}

	for _, raw := range strings.Split(md, "\n") {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		if m := versionLine.FindStringSubmatch(trimmed); m != nil {
			flushEntry()
			cur = &Entry{Version: m[1], Date: m[2]}
			continue
		}
		if cur == nil {
			continue
		}
		if m := groupLine.FindStringSubmatch(trimmed); m != nil {
			flushGroup()
			curGroup = &Group{Heading: m[1]}
			continue
		}
		if curGroup == nil {
			continue
		}
		if m := itemLine.FindStringSubmatch(trimmed); m != nil {
			curGroup.Items = append(curGroup.Items, m[1])
		}
	}
	flushEntry()
	return out, nil
}

// Slice returns entries where fromVer < v <= toVer, sorted newest-first.
// Version comparison reuses semver.Compare (dotted-numeric semver-like).
func Slice(entries map[string]Entry, fromVer, toVer string) []Entry {
	if semver.Compare(fromVer, toVer) >= 0 {
		return nil
	}
	out := make([]Entry, 0)
	for _, e := range entries {
		if semver.Compare(e.Version, fromVer) > 0 && semver.Compare(e.Version, toVer) <= 0 {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return semver.Compare(out[i].Version, out[j].Version) > 0
	})
	return out
}
