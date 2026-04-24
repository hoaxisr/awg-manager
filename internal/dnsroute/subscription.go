package dnsroute

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

var subscriptionHTTPClient = &http.Client{Timeout: 30 * time.Second}

func isSupportedSubscriptionContentType(ct string) bool {
	ct = strings.TrimSpace(strings.ToLower(ct))
	if ct == "" {
		return false
	}
	return strings.HasPrefix(ct, "text/plain") || strings.HasPrefix(ct, "application/octet-stream")
}

// fetchSubscription downloads a domain list from a URL and parses it.
func fetchSubscription(ctx context.Context, url string) ([]string, error) {
	url = normalizeGitHubURL(url)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := subscriptionHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// Accept text lists from strict and generic servers.
	ct := resp.Header.Get("Content-Type")
	if !isSupportedSubscriptionContentType(ct) {
		if strings.TrimSpace(ct) == "" {
			return nil, fmt.Errorf("сервер не указал Content-Type (нужен text/plain или application/octet-stream)")
		}
		return nil, fmt.Errorf("неподдерживаемый формат: %s (нужен text/plain или application/octet-stream)", ct)
	}

	var domains []string
	seen := make(map[string]bool)
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		d := parseDomainLine(scanner.Text())
		if d != "" && !seen[d] {
			seen[d] = true
			domains = append(domains, d)
		}
	}
	return domains, scanner.Err()
}

// parseDomainLine extracts a domain name from various list formats:
// plain domains, hosts files, adblock basic, wildcard prefixes, URLs with schemes/paths/ports.
// Returns empty string for comments, empty lines, and invalid entries.
func parseDomainLine(line string) string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "\xEF\xBB\xBF") // strip UTF-8 BOM

	// Skip empty lines and comments
	if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
		return ""
	}

	// Adblock basic format: ||domain.com^
	if strings.HasPrefix(line, "||") {
		line = strings.TrimPrefix(line, "||")
		line = strings.TrimSuffix(line, "^")
		line = strings.TrimSpace(line)
	}

	// Hosts format: "0.0.0.0 domain.com" or "127.0.0.1 domain.com"
	if strings.HasPrefix(line, "0.0.0.0") || strings.HasPrefix(line, "127.0.0.1") {
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			line = parts[1]
		}
	}

	// Strip scheme
	line = strings.TrimPrefix(line, "https://")
	line = strings.TrimPrefix(line, "http://")

	// If line is a valid CIDR, return it directly (don't strip the /prefix as path).
	if ip, cidr, err := net.ParseCIDR(line); err == nil {
		if isLocalIP(ip) {
			return ""
		}
		return cidr.String()
	}

	// Strip path
	if idx := strings.IndexByte(line, '/'); idx >= 0 {
		line = line[:idx]
	}

	// Strip wildcard prefix and leading dot (.ua → ua)
	line = strings.TrimPrefix(line, "*.")
	line = strings.TrimPrefix(line, ".")

	// Strip port
	if host, _, err := net.SplitHostPort(line); err == nil {
		line = host
	}

	// Validate
	line = strings.ToLower(strings.TrimSpace(line))
	if line == "" || line == "localhost" || strings.ContainsAny(line, " \t*") {
		return ""
	}

	// Filter private IPs
	if ip := net.ParseIP(line); ip != nil && isLocalIP(ip) {
		return ""
	}

	return line
}

// normalizeGitHubURL converts GitHub blob/tree page URLs to raw content URLs.
// github.com/{user}/{repo}/blob/{branch}/{path} → raw.githubusercontent.com/{user}/{repo}/{branch}/{path}
func normalizeGitHubURL(url string) string {
	const blobPrefix = "https://github.com/"
	if !strings.HasPrefix(url, blobPrefix) {
		return url
	}
	rest := url[len(blobPrefix):]
	// Expect: {user}/{repo}/blob/{branch}/{path...}
	parts := strings.SplitN(rest, "/", 4) // [user, repo, "blob", branch/path...]
	if len(parts) < 4 || (parts[2] != "blob" && parts[2] != "tree") {
		return url
	}
	// Reconstruct: raw.githubusercontent.com/{user}/{repo}/{branch}/{path}
	return "https://raw.githubusercontent.com/" + parts[0] + "/" + parts[1] + "/" + parts[3]
}

// validateSubscriptionURL fetches a URL and verifies it returns text/plain with parseable domains.
// Used to reject invalid URLs at create/update time, not just at refresh time.
func validateSubscriptionURL(ctx context.Context, url string) error {
	domains, err := fetchSubscription(ctx, url)
	if err != nil {
		return err
	}
	if len(domains) == 0 {
		return fmt.Errorf("список пуст — URL не содержит доменов")
	}
	return nil
}

// findNewSubscriptions returns subscriptions in updated that are not present in existing (by URL).
func findNewSubscriptions(existing, updated []Subscription) []Subscription {
	old := make(map[string]bool, len(existing))
	for _, s := range existing {
		old[s.URL] = true
	}
	var newSubs []Subscription
	for _, s := range updated {
		if !old[s.URL] {
			newSubs = append(newSubs, s)
		}
	}
	return newSubs
}

// isLocalIP returns true for loopback, private, and link-local addresses.
func isLocalIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

// mergeDomains combines manual domains and subscription results into a single deduplicated list.
// Manual domains are added first, preserving their order priority.
func mergeDomains(manual []string, subscriptionDomains [][]string) []string {
	seen := make(map[string]bool)
	var result []string

	for _, d := range manual {
		d = strings.ToLower(strings.TrimSpace(d))
		if d != "" && !seen[d] {
			seen[d] = true
			result = append(result, d)
		}
	}

	for _, domains := range subscriptionDomains {
		for _, d := range domains {
			if !seen[d] {
				seen[d] = true
				result = append(result, d)
			}
		}
	}

	return result
}
