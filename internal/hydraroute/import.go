package hydraroute

import (
	"fmt"
	"os"
	"strings"
)

// NativeRule is a domain routing rule found in domain.conf outside the managed section.
type NativeRule struct {
	Name    string
	Domains []string
	Target  string
}

// NativeIPBlock is an IP routing block found in ip.list outside the managed section.
type NativeIPBlock struct {
	Name    string
	Subnets []string
	Target  string
}

// ParseNativeDomainConf reads domain.conf and returns rules outside the managed markers.
func ParseNativeDomainConf() ([]NativeRule, error) {
	data, err := os.ReadFile(domainConfPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("hydraroute: read domain.conf: %w", err)
	}
	return parseNativeDomainConf(string(data)), nil
}

// ParseNativeIPList reads ip.list and returns blocks outside the managed markers.
func ParseNativeIPList() ([]NativeIPBlock, error) {
	data, err := os.ReadFile(ipListPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("hydraroute: read ip.list: %w", err)
	}
	return parseNativeIPList(string(data)), nil
}

// RemoveNativeFromDomainConf rewrites domain.conf keeping only the managed section.
func RemoveNativeFromDomainConf() error {
	data, err := os.ReadFile(domainConfPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("hydraroute: read domain.conf: %w", err)
	}
	cleaned := removeNativeBlocks(string(data))
	return atomicWrite(domainConfPath, cleaned)
}

// RemoveNativeFromIPList rewrites ip.list keeping only the managed section.
func RemoveNativeFromIPList() error {
	data, err := os.ReadFile(ipListPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("hydraroute: read ip.list: %w", err)
	}
	cleaned := removeNativeBlocks(string(data))
	return atomicWrite(ipListPath, cleaned)
}

// parseNativeDomainConf parses domain.conf content and returns rules outside managed markers.
//
// Format:
//
//	##RuleName
//	domain1,domain2/Target
//
// Lines beginning with a single '#' (but not '##') are comments and skipped.
// Empty lines are skipped. Content between managed markers is skipped.
func parseNativeDomainConf(content string) []NativeRule {
	lines := strings.Split(content, "\n")
	var rules []NativeRule

	inManaged := false
	var pendingName string

	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r")

		// Managed section guard.
		if line == markerStart {
			inManaged = true
			pendingName = ""
			continue
		}
		if line == markerEnd {
			inManaged = false
			continue
		}
		if inManaged {
			continue
		}

		if line == "" {
			// Empty line resets pending header.
			pendingName = ""
			continue
		}

		// Single-'#' comment (but not '##' header).
		if strings.HasPrefix(line, "#") && !strings.HasPrefix(line, "##") {
			continue
		}

		// '##Name' header line.
		if strings.HasPrefix(line, "##") {
			pendingName = strings.TrimPrefix(line, "##")
			continue
		}

		// Domain/target line: domains/Target
		if pendingName != "" {
			slash := strings.LastIndex(line, "/")
			if slash < 0 {
				// Malformed — reset.
				pendingName = ""
				continue
			}
			domainsRaw := line[:slash]
			target := line[slash+1:]
			domains := strings.Split(domainsRaw, ",")
			// Filter empty entries.
			var filtered []string
			for _, d := range domains {
				d = strings.TrimSpace(d)
				if d != "" {
					filtered = append(filtered, d)
				}
			}
			rules = append(rules, NativeRule{
				Name:    pendingName,
				Domains: filtered,
				Target:  target,
			})
			pendingName = ""
		}
	}

	return rules
}

// parseNativeIPList parses ip.list content and returns blocks outside managed markers.
//
// Format per block:
//
//	##BlockName
//	/Target
//	cidr1
//	cidr2
//	<empty line or next ##>
func parseNativeIPList(content string) []NativeIPBlock {
	lines := strings.Split(content, "\n")
	var blocks []NativeIPBlock

	inManaged := false

	type pending struct {
		name    string
		target  string
		subnets []string
		active  bool
	}
	var cur pending

	flush := func() {
		if cur.active && cur.name != "" {
			blocks = append(blocks, NativeIPBlock{
				Name:    cur.name,
				Subnets: cur.subnets,
				Target:  cur.target,
			})
		}
		cur = pending{}
	}

	for _, raw := range lines {
		line := strings.TrimRight(raw, "\r")

		// Managed section guard.
		if line == markerStart {
			flush()
			inManaged = true
			continue
		}
		if line == markerEnd {
			inManaged = false
			continue
		}
		if inManaged {
			continue
		}

		// Empty line terminates current block.
		if line == "" {
			flush()
			continue
		}

		// '##Name' header — start new block.
		if strings.HasPrefix(line, "##") {
			flush()
			cur = pending{
				name:   strings.TrimPrefix(line, "##"),
				active: true,
			}
			continue
		}

		// Single '#' comment — skip.
		if strings.HasPrefix(line, "#") {
			continue
		}

		if !cur.active {
			continue
		}

		// '/Target' line.
		if strings.HasPrefix(line, "/") {
			cur.target = strings.TrimPrefix(line, "/")
			continue
		}

		// Subnet / geoip tag line.
		cur.subnets = append(cur.subnets, line)
	}

	// Flush last block if file doesn't end with empty line.
	flush()

	return blocks
}

// removeNativeBlocks returns only the content between (and including) the managed
// markers. If no markers are present, returns an empty string.
func removeNativeBlocks(content string) string {
	startIdx := strings.Index(content, markerStart)
	endIdx := strings.Index(content, markerEnd)
	if startIdx < 0 || endIdx <= startIdx {
		return ""
	}
	end := endIdx + len(markerEnd)
	// Include trailing newline after end marker if present.
	if end < len(content) && content[end] == '\n' {
		end++
	}
	return content[startIdx:end]
}
