package hydraroute

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
)

// ExtractGeoSiteTags reads a GeoSite .dat file and returns tag names with domain counts.
func ExtractGeoSiteTags(path string) ([]GeoTag, error) {
	// GeoSite: top-level field 1 = entry (repeated). Inside entry: field 1 = country_code,
	// field 2 = domain entries (repeated LD).
	return extractTags(path, 1, 2)
}

// ExtractGeoIPTags reads a GeoIP .dat file and returns tag names with CIDR counts.
func ExtractGeoIPTags(path string) ([]GeoTag, error) {
	// GeoIP: top-level field 1 = entry (repeated). Inside entry: field 1 = country_code,
	// field 2 = CIDR entries (repeated LD).
	return extractTags(path, 1, 2)
}

// ReadFileInfo returns the file size, tag count, and any error for a geo .dat file.
//
// This opens and streams the file; avoid calling on a path where full parse is
// not needed (e.g. startup adoption). Prefer os.Stat for size-only, or the
// cached TagCount in GeoFileEntry.
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

// extractTags streams the .dat file via a 64 KB bufio.Reader, extracting only
// tag names and per-tag item counts. The file is never loaded into memory as
// a whole — per-entry payload bytes are Discarded, not allocated. This keeps
// peak RAM at ~64 KB regardless of file size (important for routers with ~256
// MB total RAM where a naive os.ReadFile on a 66 MB geosite file evicts the
// squashfs page cache and stalls NDM).
//
// ccField is the field number inside the entry submessage holding the tag name
// (country_code), countField is the field number of the repeated items
// (domains for GeoSite, CIDRs for GeoIP).
func extractTags(path string, ccField, countField int) ([]GeoTag, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	br := bufio.NewReaderSize(f, 64*1024)
	var tags []GeoTag

	for {
		fieldNum, wireType, err := readProtoTag(br)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s: top-level tag: %w", path, err)
		}

		if wireType != 2 {
			// Top-level non-length-delimited fields: skip.
			if err := skipProtoField(br, wireType); err != nil {
				return nil, fmt.Errorf("%s: skip top-level field %d: %w", path, fieldNum, err)
			}
			continue
		}

		length, err := readProtoVarint(br)
		if err != nil {
			return nil, fmt.Errorf("%s: submessage length: %w", path, err)
		}

		if fieldNum != 1 {
			// Unknown top-level submessage — discard without parsing.
			if _, err := br.Discard(int(length)); err != nil {
				return nil, fmt.Errorf("%s: discard top-level field %d: %w", path, fieldNum, err)
			}
			continue
		}

		tag, err := parseEntryStream(br, int(length), ccField, countField)
		if err != nil {
			return nil, fmt.Errorf("%s: parse entry: %w", path, err)
		}
		if tag.Name != "" {
			tags = append(tags, tag)
		}
	}

	sort.Slice(tags, func(i, j int) bool {
		return tags[i].Name < tags[j].Name
	})
	return tags, nil
}

// parseEntryStream reads exactly entryLen bytes of an entry submessage from br,
// extracting the country_code (ccField, string) and counting the repeated
// items of countField. Payload bytes are Discarded, not allocated — only the
// country_code bytes (~20 bytes) are ever held.
func parseEntryStream(br *bufio.Reader, entryLen, ccField, countField int) (GeoTag, error) {
	var tag GeoTag
	remaining := entryLen

	for remaining > 0 {
		fieldNum, wireType, n, err := readProtoTagBytes(br)
		if err != nil {
			return tag, fmt.Errorf("entry tag: %w", err)
		}
		remaining -= n

		switch wireType {
		case 0: // varint
			_, n, err := readProtoVarintBytes(br)
			if err != nil {
				return tag, fmt.Errorf("entry varint field %d: %w", fieldNum, err)
			}
			remaining -= n
			if fieldNum == countField {
				// Repeated item encoded as varint — still counts.
				tag.Count++
			}

		case 1: // 64-bit fixed
			if _, err := br.Discard(8); err != nil {
				return tag, fmt.Errorf("entry fixed64: %w", err)
			}
			remaining -= 8

		case 2: // length-delimited
			length, n, err := readProtoVarintBytes(br)
			if err != nil {
				return tag, fmt.Errorf("entry LD field %d length: %w", fieldNum, err)
			}
			remaining -= n
			if int(length) > remaining {
				return tag, fmt.Errorf("entry field %d length %d exceeds remaining %d", fieldNum, length, remaining)
			}

			switch fieldNum {
			case ccField:
				// Read name into a small heap allocation.
				nameBuf := make([]byte, length)
				if _, err := io.ReadFull(br, nameBuf); err != nil {
					return tag, fmt.Errorf("entry country_code: %w", err)
				}
				tag.Name = string(nameBuf)
			case countField:
				// Repeated item: count it, discard payload.
				tag.Count++
				if length > 0 {
					if _, err := br.Discard(int(length)); err != nil {
						return tag, fmt.Errorf("entry item discard: %w", err)
					}
				}
			default:
				if length > 0 {
					if _, err := br.Discard(int(length)); err != nil {
						return tag, fmt.Errorf("entry field %d discard: %w", fieldNum, err)
					}
				}
			}
			remaining -= int(length)

		case 5: // 32-bit fixed
			if _, err := br.Discard(4); err != nil {
				return tag, fmt.Errorf("entry fixed32: %w", err)
			}
			remaining -= 4

		default:
			return tag, fmt.Errorf("entry field %d: unsupported wire type %d", fieldNum, wireType)
		}
	}

	if remaining != 0 {
		return tag, fmt.Errorf("entry misaligned: %d bytes left over", remaining)
	}
	return tag, nil
}

// readProtoVarint reads a varint from br and returns its decoded value.
func readProtoVarint(br *bufio.Reader) (uint64, error) {
	v, _, err := readProtoVarintBytes(br)
	return v, err
}

// readProtoVarintBytes reads a varint and returns both the value and the
// number of bytes consumed. Useful when tracking position inside a bounded
// region (submessage).
func readProtoVarintBytes(br *bufio.Reader) (uint64, int, error) {
	var v uint64
	var shift uint
	var n int
	for {
		b, err := br.ReadByte()
		if err != nil {
			return 0, n, err
		}
		n++
		v |= uint64(b&0x7F) << shift
		if b < 0x80 {
			return v, n, nil
		}
		shift += 7
		if shift >= 64 {
			return 0, n, fmt.Errorf("varint overflow")
		}
	}
}

// readProtoTag reads a tag (field number + wire type) without reporting bytes.
func readProtoTag(br *bufio.Reader) (fieldNum, wireType int, err error) {
	v, err := readProtoVarint(br)
	if err != nil {
		return 0, 0, err
	}
	return int(v >> 3), int(v & 0x7), nil
}

// readProtoTagBytes reads a tag and also reports bytes consumed.
func readProtoTagBytes(br *bufio.Reader) (fieldNum, wireType, consumed int, err error) {
	v, n, err := readProtoVarintBytes(br)
	if err != nil {
		return 0, 0, n, err
	}
	return int(v >> 3), int(v & 0x7), n, nil
}

// skipProtoField skips one field value of the given wire type.
func skipProtoField(br *bufio.Reader, wireType int) error {
	switch wireType {
	case 0:
		_, err := readProtoVarint(br)
		return err
	case 1:
		_, err := br.Discard(8)
		return err
	case 2:
		length, err := readProtoVarint(br)
		if err != nil {
			return err
		}
		_, err = br.Discard(int(length))
		return err
	case 5:
		_, err := br.Discard(4)
		return err
	default:
		return fmt.Errorf("unsupported wire type %d", wireType)
	}
}
