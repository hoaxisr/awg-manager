package hydraroute

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// StreamGeoSiteTagLines invokes emit for each sing-box inline list line in a
// geosite tag without building a full []string in memory.
func StreamGeoSiteTagLines(path, tag string, emit func(line string) error) error {
	want := strings.ToUpper(strings.TrimSpace(tag))
	if want == "" {
		return fmt.Errorf("empty geosite tag")
	}
	return streamTagLines(path, want, "geosite", parseGeoSiteDomainLine, emit)
}

// StreamGeoIPTagLines invokes emit for each CIDR line in a geoip tag without
// accumulating the full tag in memory.
func StreamGeoIPTagLines(path, tag string, emit func(line string) error) error {
	want := strings.ToUpper(strings.TrimSpace(tag))
	if want == "" {
		return fmt.Errorf("empty geoip tag")
	}
	return streamTagLines(path, want, "geoip", parseGeoIPCidrLine, emit)
}

func streamTagLines(path, wantTag, kind string, parseItem itemParser, emit func(line string) error) error {
	found := false
	err := walkGeoDatEntries(path, func(br *bufio.Reader, entryLen int) error {
		name, err := streamParseEntryItems(br, entryLen, wantTag, parseItem, emit)
		if err != nil {
			return fmt.Errorf("%s: parse entry: %w", path, err)
		}
		if name != "" && strings.EqualFold(name, wantTag) {
			found = true
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%s tag %q not found in %s: %w", kind, wantTag, path, ErrGeoTagNotFound)
	}
	return nil
}

// streamParseEntryItems walks one dat entry and calls emit per matching item.
// Returns the entry country_code tag name (may be empty).
func streamParseEntryItems(br *bufio.Reader, entryLen int, wantTag string, parseItem itemParser, emit func(line string) error) (string, error) {
	var name string
	remaining := entryLen

	for remaining > 0 {
		fieldNum, wireType, n, err := readProtoTagBytes(br)
		if err != nil {
			return "", fmt.Errorf("entry tag: %w", err)
		}
		remaining -= n

		switch wireType {
		case 0:
			_, n, err := readProtoVarintBytes(br)
			if err != nil {
				return "", fmt.Errorf("entry varint field %d: %w", fieldNum, err)
			}
			remaining -= n

		case 1:
			if _, err := br.Discard(8); err != nil {
				return "", fmt.Errorf("entry fixed64: %w", err)
			}
			remaining -= 8

		case 2:
			length, n, err := readProtoVarintBytes(br)
			if err != nil {
				return "", fmt.Errorf("entry LD field %d length: %w", fieldNum, err)
			}
			remaining -= n
			if int(length) > remaining {
				return "", fmt.Errorf("entry field %d length %d exceeds remaining %d", fieldNum, length, remaining)
			}

			switch fieldNum {
			case 1:
				nameBuf := make([]byte, length)
				if _, err := io.ReadFull(br, nameBuf); err != nil {
					return "", fmt.Errorf("entry country_code: %w", err)
				}
				name = string(nameBuf)
			case 2:
				itemBuf := make([]byte, length)
				if _, err := io.ReadFull(br, itemBuf); err != nil {
					return "", fmt.Errorf("entry item: %w", err)
				}
				if name != "" && strings.EqualFold(name, wantTag) {
					line, ok, err := parseItem(itemBuf)
					if err != nil {
						return "", err
					}
					if ok && line != "" {
						if err := emit(line); err != nil {
							return "", err
						}
					}
				}
			default:
				if length > 0 {
					if _, err := br.Discard(int(length)); err != nil {
						return "", fmt.Errorf("entry field %d discard: %w", fieldNum, err)
					}
				}
			}
			remaining -= int(length)

		case 5:
			if _, err := br.Discard(4); err != nil {
				return "", fmt.Errorf("entry fixed32: %w", err)
			}
			remaining -= 4

		default:
			return "", fmt.Errorf("entry field %d: unsupported wire type %d", fieldNum, wireType)
		}
	}

	if remaining != 0 {
		return "", fmt.Errorf("entry misaligned: %d bytes left over", remaining)
	}
	return name, nil
}
