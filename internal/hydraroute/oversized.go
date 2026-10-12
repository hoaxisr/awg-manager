package hydraroute

import (
	"context"
	"errors"
	"strings"
)

// Errors returned by RemoveOversizedTag that callers map to client errors.
var (
	// ErrOversizedTagNotFound: the tag is not in HR Neo's service section.
	ErrOversizedTagNotFound = errors.New("tag is not in the disabled tags list")
	// ErrInvalidOversizedTag: the tag name is empty (or only `geoip:`).
	ErrInvalidOversizedTag = errors.New("tag name must not be empty")
	// ErrNotInstalled: HR Neo is not installed, there is no ip.list to edit.
	ErrNotInstalled = errors.New("HydraRoute Neo is not installed")
)

// OversizedTag describes a single geoip tag that HR Neo excluded from
// routing because its entry count exceeds IpsetMaxElem. Count is -1 when
// the tag is no longer present in any installed .dat file.
type OversizedTag struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
	File  string `json:"file"`
}

// OversizedTags reads the current ip.list, extracts service-block tag
// names, and enriches each with its live entry count from the installed
// geoip .dat files.
func (s *Service) OversizedTags(ctx context.Context) ([]OversizedTag, error) {
	rs, err := s.ListRuleSet()
	if err != nil {
		return nil, err
	}
	names := rs.Oversized

	gds := s.GetGeoData()
	result := make([]OversizedTag, 0, len(names))
	for _, full := range names {
		bare := strings.TrimPrefix(full, "geoip:")

		count := -1
		file := ""
		if gds != nil {
			for _, entry := range gds.List() {
				if entry.Type != "geoip" {
					continue
				}
				tags, err := gds.GetTags(entry.Path)
				if err != nil {
					continue
				}
				for _, t := range tags {
					if strings.EqualFold(t.Name, bare) {
						count = t.Count
						file = entry.Path
						break
					}
				}
				if count >= 0 {
					break
				}
			}
		}

		result = append(result, OversizedTag{Name: full, Count: count, File: file})
	}
	return result, nil
}

// RemoveOversizedTag drops a tag from HR Neo's `##impossible to use` section
// in ip.list. HR Neo only appends to the section and does not record which
// rule a tag came from, so it cannot be put back automatically: to route the
// tag again, add it to a rule.
//
// The file is edited as text (removeOversizedTag): only the tag's lines
// leave, and an emptied section goes with its header. Rules, comments and
// anything else in ip.list stay byte-for-byte — the rules are deliberately
// not regenerated from the RuleSet, which would merge domain.conf into them.
// domain.conf is not touched and HR Neo is not restarted — it ignores the
// section anyway (`#/` target).
func (s *Service) RemoveOversizedTag(name string) error {
	want := normalizeOversizedTag(name)
	if want == "geoip:" {
		return ErrInvalidOversizedTag
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.status.Installed {
		return ErrNotInstalled
	}
	content, err := readOrEmpty(ipListPath)
	if err != nil {
		return err
	}
	updated, found := removeOversizedTag(content, want)
	if !found {
		return ErrOversizedTagNotFound
	}
	if err := WriteWholeFile(ipListPath, updated); err != nil {
		return err
	}
	s.appLog.Info("remove-oversized-tag", want, "removed from ip.list service section")
	return nil
}

// normalizeOversizedTag brings a tag to the form HR Neo writes in the
// service section: lower-case, with the `geoip:` prefix.
func normalizeOversizedTag(name string) string {
	t := strings.ToLower(strings.TrimSpace(name))
	return "geoip:" + strings.TrimPrefix(t, "geoip:")
}
