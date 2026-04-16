package hydraroute

import (
	"strings"
)

// parseDomainConf reads a domain.conf body and returns each `## Name` block
// followed by a `domains/iface` line as a ManagedEntry. Lines that don't
// match the format are silently skipped.
func parseDomainConf(content string) []ManagedEntry {
	var entries []ManagedEntry
	var pendingName string

	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimRight(raw, "\r")
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "##") {
			pendingName = strings.TrimSpace(strings.TrimPrefix(line, "##"))
			continue
		}

		// Single-'#' comment — skip.
		if strings.HasPrefix(line, "#") {
			continue
		}

		if pendingName == "" {
			continue
		}

		slash := strings.LastIndex(line, "/")
		if slash < 0 {
			pendingName = ""
			continue
		}
		domains := splitNonEmpty(line[:slash], ",")
		entries = append(entries, ManagedEntry{
			ListName: pendingName,
			Domains:  domains,
			Iface:    line[slash+1:],
		})
		pendingName = ""
	}

	return entries
}

// parseIPList reads an ip.list body and returns:
//   - regular rule entries: blocks with a `/Target` line
//   - oversized tag names: entries inside a service block whose target line
//     starts with `#/` (HR Neo's 'disabled interface' marker, e.g.
//     `#/Too-big-geoip-tag`). Only `geoip:TAG` lines in such blocks are
//     collected; other lines are discarded.
//
// Empty lines or a new `##` header terminate the current block.
func parseIPList(content string) (entries []ManagedEntry, oversized []string) {
	var cur ManagedEntry
	active := false
	service := false

	flush := func() {
		if active && !service && cur.ListName != "" && len(cur.Subnets) > 0 {
			entries = append(entries, cur)
		}
		cur = ManagedEntry{}
		active = false
		service = false
	}

	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimRight(raw, "\r")

		if strings.HasPrefix(line, "##") {
			flush()
			cur = ManagedEntry{ListName: strings.TrimSpace(strings.TrimPrefix(line, "##"))}
			active = true
			continue
		}

		if line == "" {
			flush()
			continue
		}

		// Disabled-target marker ('#/...') turns the current block into a
		// service block — its body lines that look like geoip tags go into
		// the oversized slice instead of the regular entries.
		if strings.HasPrefix(line, "#/") {
			service = true
			continue
		}

		if strings.HasPrefix(line, "#") {
			continue
		}

		if !active {
			continue
		}

		if service {
			if strings.HasPrefix(line, "geoip:") {
				oversized = append(oversized, line)
			}
			continue
		}

		if strings.HasPrefix(line, "/") {
			cur.Iface = strings.TrimPrefix(line, "/")
			continue
		}

		cur.Subnets = append(cur.Subnets, line)
	}
	flush()

	return entries, oversized
}

// splitNonEmpty splits s by sep, trims entries, drops empties.
func splitNonEmpty(s, sep string) []string {
	parts := strings.Split(s, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
