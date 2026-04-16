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
