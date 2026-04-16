package hydraroute

import (
	"context"
	"strings"
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
	_, names, err := s.ListRules()
	if err != nil {
		return nil, err
	}

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
