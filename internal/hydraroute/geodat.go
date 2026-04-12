package hydraroute

import (
	"encoding/binary"
	"fmt"
	"os"
	"sort"
)

// ExtractGeoSiteTags reads a GeoSite .dat file and returns tag names with domain counts.
func ExtractGeoSiteTags(path string) ([]GeoTag, error) {
	// GeoSite: field 1 = country_code (string), field 2 = domain entries (repeated LD)
	return extractTags(path, 1, 2)
}

// ExtractGeoIPTags reads a GeoIP .dat file and returns tag names with CIDR counts.
func ExtractGeoIPTags(path string) ([]GeoTag, error) {
	// GeoIP: field 1 = country_code (string), field 2 = CIDR entries (repeated LD)
	return extractTags(path, 1, 2)
}

// ReadFileInfo returns the file size, tag count, and any error for a geo .dat file.
func ReadFileInfo(path string, fileType string) (size int64, tagCount int, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, 0, fmt.Errorf("stat %s: %w", path, err)
	}
	size = info.Size()

	var tags []GeoTag
	switch fileType {
	case "geosite":
		tags, err = ExtractGeoSiteTags(path)
	case "geoip":
		tags, err = ExtractGeoIPTags(path)
	default:
		return size, 0, fmt.Errorf("unknown file type: %s", fileType)
	}
	if err != nil {
		return size, 0, err
	}

	return size, len(tags), nil
}

// extractTags is the shared implementation for both GeoSite and GeoIP parsing.
// ccField is the field number for country_code, countField for the repeated entries.
func extractTags(path string, ccField, countField int) ([]GeoTag, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var tags []GeoTag
	pos := 0
	for pos < len(data) {
		fieldNum, wireType, n := readTag(data[pos:])
		if n <= 0 {
			break
		}
		pos += n

		if wireType != 2 {
			// Skip non-length-delimited top-level fields
			skip := skipField(data[pos:], wireType)
			if skip <= 0 {
				break
			}
			pos += skip
			continue
		}

		// Read length of the submessage
		length, n2 := readVarint(data[pos:])
		if n2 <= 0 || pos+n2+int(length) > len(data) {
			break
		}
		pos += n2

		if fieldNum == 1 {
			// Top-level field 1: entry submessage
			entryData := data[pos : pos+int(length)]
			tag := parseEntry(entryData, ccField, countField)
			if tag.Name != "" {
				tags = append(tags, tag)
			}
		}
		pos += int(length)
	}

	sort.Slice(tags, func(i, j int) bool {
		return tags[i].Name < tags[j].Name
	})

	return tags, nil
}

// parseEntry parses a single entry submessage and extracts the tag name and item count.
func parseEntry(data []byte, ccField, countField int) GeoTag {
	var tag GeoTag
	pos := 0
	for pos < len(data) {
		fieldNum, wireType, n := readTag(data[pos:])
		if n <= 0 {
			break
		}
		pos += n

		if wireType == 2 {
			length, n2 := readVarint(data[pos:])
			if n2 <= 0 || pos+n2+int(length) > len(data) {
				break
			}
			pos += n2

			if fieldNum == ccField {
				tag.Name = string(data[pos : pos+int(length)])
			} else if fieldNum == countField {
				tag.Count++
			}

			pos += int(length)
		} else if wireType == 0 {
			_, n2 := readVarint(data[pos:])
			if n2 <= 0 {
				break
			}
			if fieldNum == ccField {
				// country_code is a string (wire type 2), not varint — skip
			} else if fieldNum == countField {
				tag.Count++
			}
			pos += n2
		} else {
			skip := skipField(data[pos:], wireType)
			if skip <= 0 {
				break
			}
			pos += skip
		}
	}
	return tag
}

// readTag reads a protobuf tag (field number + wire type) encoded as a varint.
// Returns fieldNum, wireType, and bytes consumed. Returns 0 consumed on error.
func readTag(data []byte) (fieldNum int, wireType int, consumed int) {
	v, n := readVarint(data)
	if n <= 0 {
		return 0, 0, 0
	}
	return int(v >> 3), int(v & 0x7), n
}

// readVarint reads a protobuf varint from data.
// Returns the value and bytes consumed. Returns 0 consumed on error.
func readVarint(data []byte) (uint64, int) {
	v, n := binary.Uvarint(data)
	if n <= 0 {
		return 0, 0
	}
	return v, n
}

// skipField skips past a field value of the given wire type.
// Returns the number of bytes skipped, or 0 on error.
func skipField(data []byte, wireType int) int {
	switch wireType {
	case 0: // varint
		_, n := readVarint(data)
		return n
	case 1: // 64-bit
		if len(data) < 8 {
			return 0
		}
		return 8
	case 2: // length-delimited
		length, n := readVarint(data)
		if n <= 0 || n+int(length) > len(data) {
			return 0
		}
		return n + int(length)
	case 5: // 32-bit
		if len(data) < 4 {
			return 0
		}
		return 4
	default:
		return 0
	}
}
