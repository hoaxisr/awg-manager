package updater

import (
	"fmt"
	"regexp"
	"strings"
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
	return nil, fmt.Errorf("not implemented")
}

// Slice returns entries where fromVer < v <= toVer, sorted newest-first by
// the order they appear in the source map (callers should prefer the sort
// emitted by the parser, which preserves file order — newest first).
func Slice(entries map[string]Entry, fromVer, toVer string) []Entry {
	return nil
}

// suppress unused-import complaints until implementations land
var _ = strings.TrimSpace
