package updater

import (
	"reflect"
	"testing"
)

func TestParseChangelog_SingleVersion(t *testing.T) {
	md := `# Changelog

## [2.8.0] - 2026-04-17

### Added
- feat: first feature
- feat: second feature

### Fixed
- fix: bug
`
	got, err := ParseChangelog(md)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Entry{
		"2.8.0": {
			Version: "2.8.0",
			Date:    "2026-04-17",
			Groups: []Group{
				{Heading: "Added", Items: []string{"feat: first feature", "feat: second feature"}},
				{Heading: "Fixed", Items: []string{"fix: bug"}},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestParseChangelog_MultipleVersions(t *testing.T) {
	md := `# Changelog

## [2.8.0] - 2026-04-17

### Added
- feat: latest

## [2.7.11] - 2026-04-16

### Fixed
- fix: older
`
	got, err := ParseChangelog(md)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 versions, got %d", len(got))
	}
	if got["2.8.0"].Groups[0].Items[0] != "feat: latest" {
		t.Errorf("2.8.0 wrong: %+v", got["2.8.0"])
	}
	if got["2.7.11"].Groups[0].Items[0] != "fix: older" {
		t.Errorf("2.7.11 wrong: %+v", got["2.7.11"])
	}
}

func TestParseChangelog_EmptyGroupsDropped(t *testing.T) {
	md := `## [1.0.0] - 2026-01-01

### Added

### Fixed
- fix: real
`
	got, _ := ParseChangelog(md)
	if len(got["1.0.0"].Groups) != 1 {
		t.Errorf("empty 'Added' must not produce a group: %+v", got["1.0.0"].Groups)
	}
	if got["1.0.0"].Groups[0].Heading != "Fixed" {
		t.Errorf("unexpected heading: %+v", got["1.0.0"].Groups)
	}
}

func TestParseChangelog_StarBulletsAndWhitespace(t *testing.T) {
	md := "## [1.0.0] - 2026-01-01\n\n### Added\n\n*  starred with trailing space  \n-   dashed\n"
	got, _ := ParseChangelog(md)
	items := got["1.0.0"].Groups[0].Items
	if len(items) != 2 || items[0] != "starred with trailing space" || items[1] != "dashed" {
		t.Errorf("items = %+v", items)
	}
}

func TestParseChangelog_UnknownHeadingPreserved(t *testing.T) {
	md := `## [1.0.0] - 2026-01-01

### Custom
- item
`
	got, _ := ParseChangelog(md)
	if got["1.0.0"].Groups[0].Heading != "Custom" {
		t.Errorf("unknown heading must pass through, got %+v", got["1.0.0"].Groups)
	}
}

func TestParseChangelog_TextBeforeFirstVersionIgnored(t *testing.T) {
	md := `# Changelog

Some intro paragraph.

- a stray bullet

## [1.0.0] - 2026-01-01

### Fixed
- fix: one
`
	got, _ := ParseChangelog(md)
	if len(got) != 1 {
		t.Errorf("intro must be ignored, got %+v", got)
	}
}

func TestParseChangelog_GroupBeforeVersionIgnored(t *testing.T) {
	md := `### Added
- item without a version
`
	got, _ := ParseChangelog(md)
	if len(got) != 0 {
		t.Errorf("group without version must not emit an entry: %+v", got)
	}
}

func TestParseChangelog_Empty(t *testing.T) {
	got, err := ParseChangelog("")
	if err != nil || len(got) != 0 {
		t.Errorf("empty input should be (empty map, nil): %v %v", got, err)
	}
}
