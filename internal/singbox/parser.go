package singbox

import (
	"fmt"
	"strings"
)

// Parse dispatches to the protocol-specific parser based on URI scheme.
func Parse(link string) (*ParsedOutbound, error) {
	link = strings.TrimSpace(link)
	switch {
	case strings.HasPrefix(link, "vless://"):
		return parseVLESS(link)
	case strings.HasPrefix(link, "hysteria2://"), strings.HasPrefix(link, "hy2://"):
		return parseHysteria2(link)
	case strings.HasPrefix(link, "naive+"):
		return parseNaive(link)
	case strings.HasPrefix(link, "vpn://"):
		return parseAmneziaVPN(link)
	default:
		return nil, fmt.Errorf("unsupported link scheme: %.20s", link)
	}
}

// BatchError records which input line failed to parse.
type BatchError struct {
	Line  int
	Input string
	Err   error
}

func (e BatchError) Error() string {
	return fmt.Sprintf("line %d: %v", e.Line, e.Err)
}

// ParseBatch parses multi-line text, one link per line. Blank lines ignored.
func ParseBatch(text string) ([]*ParsedOutbound, []BatchError) {
	var ok []*ParsedOutbound
	var errs []BatchError
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		p, err := Parse(line)
		if err != nil {
			errs = append(errs, BatchError{Line: i + 1, Input: line, Err: err})
			continue
		}
		ok = append(ok, p)
	}
	return ok, errs
}
