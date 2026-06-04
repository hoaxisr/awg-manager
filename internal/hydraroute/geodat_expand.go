package hydraroute

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"
)

const maxGeoExpandLines = 100_000

// ExtractGeoSiteTagLines returns sing-box inline list lines for a geosite tag.
func ExtractGeoSiteTagLines(path, tag string) ([]string, error) {
	want := strings.ToUpper(strings.TrimSpace(tag))
	if want == "" {
		return nil, fmt.Errorf("empty geosite tag")
	}
	return extractTagLines(path, want, "geosite", parseGeoSiteDomainLine)
}

// ExtractGeoIPTagLines returns sing-box inline list lines (CIDR) for a geoip tag.
func ExtractGeoIPTagLines(path, tag string) ([]string, error) {
	want := strings.ToUpper(strings.TrimSpace(tag))
	if want == "" {
		return nil, fmt.Errorf("empty geoip tag")
	}
	return extractTagLines(path, want, "geoip", parseGeoIPCidrLine)
}

type itemParser func(payload []byte) (string, bool, error)

func extractTagLines(path, wantTag, kind string, parseItem itemParser) ([]string, error) {
	var lines []string
	found := false

	err := walkGeoDatEntries(path, func(br *bufio.Reader, entryLen int) error {
		entryLines, name, err := parseEntryItems(br, entryLen, wantTag, parseItem)
		if err != nil {
			return fmt.Errorf("%s: parse entry: %w", path, err)
		}
		if name == "" || !strings.EqualFold(name, wantTag) {
			return nil
		}
		found = true
		lines = append(lines, entryLines...)
		if len(lines) > maxGeoExpandLines {
			return fmt.Errorf("%s tag %q: exceeds %d items", kind, wantTag, maxGeoExpandLines)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	if !found {
		return nil, fmt.Errorf("%s tag %q not found in %s", kind, wantTag, path)
	}
	return lines, nil
}

func parseEntryItems(br *bufio.Reader, entryLen int, wantTag string, parseItem itemParser) ([]string, string, error) {
	var name string
	var lines []string
	remaining := entryLen

	for remaining > 0 {
		fieldNum, wireType, n, err := readProtoTagBytes(br)
		if err != nil {
			return nil, "", fmt.Errorf("entry tag: %w", err)
		}
		remaining -= n

		switch wireType {
		case 0:
			_, n, err := readProtoVarintBytes(br)
			if err != nil {
				return nil, "", fmt.Errorf("entry varint field %d: %w", fieldNum, err)
			}
			remaining -= n

		case 1:
			if _, err := br.Discard(8); err != nil {
				return nil, "", fmt.Errorf("entry fixed64: %w", err)
			}
			remaining -= 8

		case 2:
			length, n, err := readProtoVarintBytes(br)
			if err != nil {
				return nil, "", fmt.Errorf("entry LD field %d length: %w", fieldNum, err)
			}
			remaining -= n
			if int(length) > remaining {
				return nil, "", fmt.Errorf("entry field %d length %d exceeds remaining %d", fieldNum, length, remaining)
			}

			switch fieldNum {
			case 1:
				nameBuf := make([]byte, length)
				if _, err := io.ReadFull(br, nameBuf); err != nil {
					return nil, "", fmt.Errorf("entry country_code: %w", err)
				}
				name = string(nameBuf)
			case 2:
				itemBuf := make([]byte, length)
				if _, err := io.ReadFull(br, itemBuf); err != nil {
					return nil, "", fmt.Errorf("entry item: %w", err)
				}
				if name != "" && strings.EqualFold(name, wantTag) {
					line, ok, err := parseItem(itemBuf)
					if err != nil {
						return nil, "", err
					}
					if ok && line != "" {
						lines = append(lines, line)
					}
				}
			default:
				if length > 0 {
					if _, err := br.Discard(int(length)); err != nil {
						return nil, "", fmt.Errorf("entry field %d discard: %w", fieldNum, err)
					}
				}
			}
			remaining -= int(length)

		case 5:
			if _, err := br.Discard(4); err != nil {
				return nil, "", fmt.Errorf("entry fixed32: %w", err)
			}
			remaining -= 4

		default:
			return nil, "", fmt.Errorf("entry field %d: unsupported wire type %d", fieldNum, wireType)
		}
	}

	if remaining != 0 {
		return nil, "", fmt.Errorf("entry misaligned: %d bytes left over", remaining)
	}
	return lines, name, nil
}

// parseGeoSiteDomainLine converts a v2fly Domain submessage to an inline list line.
func parseGeoSiteDomainLine(data []byte) (string, bool, error) {
	var domainType int
	var value string
	off := 0

	for off < len(data) {
		fieldNum, wireType, n, err := readProtoTagBytesFromSlice(data[off:])
		if err != nil {
			return "", false, fmt.Errorf("domain tag: %w", err)
		}
		off += n

		switch wireType {
		case 0:
			v, consumed, err := readProtoVarintFromSlice(data[off:])
			if err != nil {
				return "", false, err
			}
			off += consumed
			if fieldNum == 1 {
				domainType = int(v)
			}

		case 2:
			length, consumed, err := readProtoVarintFromSlice(data[off:])
			if err != nil {
				return "", false, err
			}
			off += consumed
			if off+int(length) > len(data) {
				return "", false, fmt.Errorf("domain field %d length overflow", fieldNum)
			}
			if fieldNum == 2 {
				value = string(data[off : off+int(length)])
			}
			off += int(length)

		default:
			return "", false, fmt.Errorf("domain field %d: unsupported wire type %d", fieldNum, wireType)
		}
	}

	value = strings.TrimSpace(value)
	if value == "" {
		return "", false, nil
	}

	switch domainType {
	case 0: // Plain
		return value, true, nil
	case 1: // Regex
		return "domain_regex:" + value, true, nil
	case 2: // RootDomain
		if strings.HasPrefix(value, ".") {
			return value, true, nil
		}
		return "." + value, true, nil
	case 3: // Full
		return value, true, nil
	default:
		return value, true, nil
	}
}

func parseGeoIPCidrLine(data []byte) (string, bool, error) {
	var ip []byte
	var prefix uint32
	off := 0

	for off < len(data) {
		fieldNum, wireType, n, err := readProtoTagBytesFromSlice(data[off:])
		if err != nil {
			return "", false, fmt.Errorf("cidr tag: %w", err)
		}
		off += n

		switch wireType {
		case 0:
			v, consumed, err := readProtoVarintFromSlice(data[off:])
			if err != nil {
				return "", false, err
			}
			off += consumed
			if fieldNum == 2 {
				prefix = uint32(v)
			}

		case 2:
			length, consumed, err := readProtoVarintFromSlice(data[off:])
			if err != nil {
				return "", false, err
			}
			off += consumed
			if off+int(length) > len(data) {
				return "", false, fmt.Errorf("cidr field %d length overflow", fieldNum)
			}
			if fieldNum == 1 {
				ip = append([]byte(nil), data[off:off+int(length)]...)
			}
			off += int(length)

		default:
			return "", false, fmt.Errorf("cidr field %d: unsupported wire type %d", fieldNum, wireType)
		}
	}

	if len(ip) == 0 {
		return "", false, nil
	}
	ipStr := net.IP(ip).String()
	if prefix == 0 {
		if strings.Contains(ipStr, ":") {
			return ipStr + "/128", true, nil
		}
		return ipStr + "/32", true, nil
	}
	return fmt.Sprintf("%s/%d", ipStr, prefix), true, nil
}

func readProtoTagBytesFromSlice(b []byte) (fieldNum, wireType, consumed int, err error) {
	v, n, err := readProtoVarintFromSlice(b)
	if err != nil {
		return 0, 0, n, err
	}
	return int(v >> 3), int(v & 0x7), n, nil
}

func readProtoVarintFromSlice(b []byte) (uint64, int, error) {
	var v uint64
	var shift uint
	for i, byt := range b {
		v |= uint64(byt&0x7F) << shift
		if byt < 0x80 {
			return v, i + 1, nil
		}
		shift += 7
		if shift >= 64 {
			return 0, i + 1, fmt.Errorf("varint overflow")
		}
	}
	return 0, len(b), io.ErrUnexpectedEOF
}

// ExpandGeoTag finds tag in tracked geo files and returns inline list lines.
func (s *GeoDataStore) ExpandGeoTag(kind, tag string) (lines []string, filePath string, err error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return nil, "", fmt.Errorf("empty tag")
	}
	if kind != "geosite" && kind != "geoip" {
		return nil, "", fmt.Errorf("unknown kind %q", kind)
	}

	s.mu.RLock()
	paths := make([]string, 0, len(s.entries))
	for _, e := range s.entries {
		if e.Type == kind {
			paths = append(paths, e.Path)
		}
	}
	s.mu.RUnlock()

	var lastErr error
	for _, path := range paths {
		var got []string
		switch kind {
		case "geosite":
			got, err = ExtractGeoSiteTagLines(path, tag)
		case "geoip":
			got, err = ExtractGeoIPTagLines(path, tag)
		}
		if err == nil {
			return got, path, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, "", lastErr
	}
	return nil, "", fmt.Errorf("%s tag %q not found in any file", kind, tag)
}
