package pagescan

import (
	"bytes"
	"html"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/ipinspect"
)

const defaultMaxHosts = 40

type extractedHost struct {
	Host    string
	Sources []string
}

var allowedLinkRels = map[string]string{
	"preconnect":       "preconnect",
	"dns-prefetch":     "preconnect",
	"stylesheet":       "style",
	"preload":          "link",
	"modulepreload":    "link",
	"icon":             "icon",
	"shortcut":         "icon",
	"apple-touch-icon": "icon",
	"mask-icon":        "icon",
	"manifest":         "link",
}

var cspResourceDirectives = map[string]bool{
	"default-src":     true,
	"script-src":      true,
	"script-src-elem": true,
	"connect-src":     true,
	"img-src":         true,
	"style-src":       true,
	"style-src-elem":  true,
	"font-src":        true,
	"frame-src":       true,
	"media-src":       true,
	"worker-src":      true,
	"child-src":       true,
	"manifest-src":    true,
	"object-src":      true,
}

type resourceCollector struct {
	order         []string
	sourcesByHost map[string][]string
	seenSource    map[string]map[string]bool
	allowPrivate  bool
}

func newResourceCollector(allowPrivate bool) *resourceCollector {
	return &resourceCollector{
		sourcesByHost: make(map[string][]string),
		seenSource:    make(map[string]map[string]bool),
		allowPrivate:  allowPrivate,
	}
}

func (c *resourceCollector) addHost(host, source string) {
	host = normalizeExtractedHost(host)
	if host == "" {
		return
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if !c.allowPrivate {
			_, scope := ipinspect.ClassifyAddr(addr)
			if scope != "public" {
				return
			}
		}
	} else if !isAcceptableHostname(host) {
		return
	}

	if _, ok := c.sourcesByHost[host]; !ok {
		c.order = append(c.order, host)
		c.seenSource[host] = make(map[string]bool)
	}
	if source != "" && !c.seenSource[host][source] {
		c.seenSource[host][source] = true
		c.sourcesByHost[host] = append(c.sourcesByHost[host], source)
	}
}

func (c *resourceCollector) addURLRef(raw string, base *url.URL, source string) {
	raw = strings.TrimSpace(html.UnescapeString(raw))
	if raw == "" {
		return
	}
	lower := strings.ToLower(raw)
	for _, skipPrefix := range []string{"data:", "blob:", "javascript:", "mailto:", "tel:", "about:", "#"} {
		if strings.HasPrefix(lower, skipPrefix) {
			return
		}
	}

	if strings.HasPrefix(raw, "//") && base != nil && base.Scheme != "" {
		raw = base.Scheme + ":" + raw
	}

	ref, err := url.Parse(raw)
	if err != nil {
		return
	}
	if ref.Scheme != "" && ref.Scheme != "http" && ref.Scheme != "https" {
		return
	}
	if ref.Host == "" && base != nil {
		ref = base.ResolveReference(ref)
	}
	if ref.Scheme != "http" && ref.Scheme != "https" {
		return
	}
	c.addHost(ref.Hostname(), source)
}

func (c *resourceCollector) addCSP(policy string) {
	// Multiple policies may be joined with commas.
	for _, policyPart := range strings.Split(policy, ",") {
		for _, directiveRaw := range strings.Split(policyPart, ";") {
			fields := strings.Fields(strings.TrimSpace(directiveRaw))
			if len(fields) < 2 {
				continue
			}
			directive := strings.ToLower(fields[0])
			if !cspResourceDirectives[directive] {
				continue
			}
			for _, token := range fields[1:] {
				c.addCSPToken(token)
			}
		}
	}
}

func (c *resourceCollector) addCSPToken(token string) {
	token = strings.TrimSpace(token)
	if token == "" {
		return
	}
	if strings.HasPrefix(token, "'") || strings.HasSuffix(token, "'") {
		return
	}
	lower := strings.ToLower(token)
	if lower == "*" || strings.HasSuffix(lower, ":") {
		// e.g. "*", "https:", "data:", "blob:", "ws:", "wss:"
		return
	}
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		u, err := url.Parse(token)
		if err != nil {
			return
		}
		host := strings.TrimPrefix(u.Hostname(), "*.")
		c.addHost(host, "csp")
		return
	}
	if idx := strings.Index(token, "://"); idx >= 0 {
		// wss://example.com or similar; strip scheme only if ws/wss
		scheme := lower[:idx]
		if scheme != "ws" && scheme != "wss" {
			return
		}
		token = token[idx+3:]
	}
	if slash := strings.IndexByte(token, '/'); slash >= 0 {
		token = token[:slash]
	}
	token = strings.TrimPrefix(token, "*.")
	if token == "" || strings.Contains(token, "*") {
		return
	}
	if strings.HasPrefix(token, "[") && strings.Contains(token, "]") {
		if closeIdx := strings.IndexByte(token, ']'); closeIdx > 1 {
			token = token[1:closeIdx]
		}
	} else if colon := strings.LastIndexByte(token, ':'); colon > 0 && strings.Count(token, ":") == 1 {
		portPart := token[colon+1:]
		if portPart == "*" {
			token = token[:colon]
		} else if _, err := strconv.Atoi(portPart); err == nil {
			token = token[:colon]
		}
	}
	c.addHost(token, "csp")
}

func (c *resourceCollector) addLinkHeader(headerVal string, base *url.URL) {
	for _, entry := range splitHTTPHeaderEntries(headerVal) {
		entry = strings.TrimSpace(entry)
		if !strings.HasPrefix(entry, "<") {
			continue
		}
		endURL := strings.IndexByte(entry, '>')
		if endURL <= 1 {
			continue
		}
		linkTarget := strings.TrimSpace(entry[1:endURL])
		params := entry[endURL+1:]
		relSource := ""
		for _, param := range strings.Split(params, ";") {
			param = strings.TrimSpace(param)
			eq := strings.IndexByte(param, '=')
			if eq <= 0 {
				continue
			}
			k := strings.ToLower(strings.TrimSpace(param[:eq]))
			if k != "rel" {
				continue
			}
			v := strings.Trim(strings.TrimSpace(param[eq+1:]), `"'`)
			relSource = mapRelToSource(v)
			if relSource != "" {
				break
			}
		}
		if relSource != "" {
			c.addURLRef(linkTarget, base, relSource)
		}
	}
}

func (c *resourceCollector) results(maxHosts int) (hosts []extractedHost, total int, truncated bool) {
	if maxHosts <= 0 {
		maxHosts = defaultMaxHosts
	}
	total = len(c.order)
	limit := total
	if total > maxHosts {
		limit = maxHosts
		truncated = true
	}
	hosts = make([]extractedHost, 0, limit)
	for _, h := range c.order[:limit] {
		hosts = append(hosts, extractedHost{
			Host:    h,
			Sources: append([]string(nil), c.sourcesByHost[h]...),
		})
	}
	return hosts, total, truncated
}

func extractResources(
	requestedURL *url.URL,
	finalURL *url.URL,
	headers http.Header,
	redirectURLs []*url.URL,
	body []byte,
	contentType string,
	maxHosts int,
	allowPrivate bool,
) ([]extractedHost, int, bool) {
	c := newResourceCollector(allowPrivate)

	if requestedURL != nil {
		c.addHost(requestedURL.Hostname(), "document")
	}
	for _, ru := range redirectURLs {
		if ru != nil {
			c.addHost(ru.Hostname(), "redirect")
		}
	}
	baseURL := finalURL
	if baseURL == nil {
		baseURL = requestedURL
	}
	if finalURL != nil {
		c.addHost(finalURL.Hostname(), "document")
	}

	if headers != nil {
		for _, linkVal := range headers.Values("Link") {
			c.addLinkHeader(linkVal, baseURL)
		}
		for _, cspVal := range headers.Values("Content-Security-Policy") {
			c.addCSP(cspVal)
		}
		for _, cspROVal := range headers.Values("Content-Security-Policy-Report-Only") {
			c.addCSP(cspROVal)
		}
	}

	if len(body) > 0 && isHTMLContentType(contentType) {
		c.scanHTML(body, baseURL)
	}

	return c.results(maxHosts)
}

func isHTMLContentType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if ct == "" {
		return true
	}
	return strings.Contains(ct, "html") || strings.Contains(ct, "xhtml") || strings.Contains(ct, "xml")
}

func mapRelToSource(relAttr string) string {
	for _, token := range strings.Fields(strings.ToLower(relAttr)) {
		if src, ok := allowedLinkRels[token]; ok {
			return src
		}
	}
	return ""
}

func (c *resourceCollector) scanHTML(data []byte, base *url.URL) {
	effectiveBase := base
	baseLocked := false
	i := 0
	n := len(data)

	for i < n {
		lt := bytes.IndexByte(data[i:], '<')
		if lt < 0 {
			break
		}
		i += lt
		if i+1 >= n {
			break
		}

		// Skip HTML comments <!-- ... -->
		if bytes.HasPrefix(data[i:], []byte("<!--")) {
			end := bytes.Index(data[i+4:], []byte("-->"))
			if end < 0 {
				break
			}
			i += 4 + end + 3
			continue
		}

		// Skip declarations / CDATA such as <!DOCTYPE ...> or <![CDATA[...]]>
		if data[i+1] == '!' || data[i+1] == '?' {
			if bytes.HasPrefix(data[i:], []byte("<![CDATA[")) {
				end := bytes.Index(data[i+9:], []byte("]]>"))
				if end < 0 {
					break
				}
				i += 9 + end + 3
				continue
			}
			gt := bytes.IndexByte(data[i+1:], '>')
			if gt < 0 {
				break
			}
			i += 1 + gt + 1
			continue
		}

		// Closing tag </...>
		if data[i+1] == '/' {
			gt := bytes.IndexByte(data[i+2:], '>')
			if gt < 0 {
				break
			}
			i += 2 + gt + 1
			continue
		}

		tagName, attrs, nextIdx := parseOpeningTag(data, i)
		if tagName == "" {
			i++
			continue
		}
		i = nextIdx

		switch tagName {
		case "base":
			if !baseLocked {
				if href, ok := attrs["href"]; ok {
					resolved := resolveBaseURL(href, effectiveBase)
					if resolved != nil {
						effectiveBase = resolved
						baseLocked = true
					}
				}
			}
		case "script":
			if src, ok := attrs["src"]; ok {
				c.addURLRef(src, effectiveBase, "script")
			}
			if styleVal, ok := attrs["style"]; ok {
				c.scanCSSURLs([]byte(styleVal), effectiveBase)
			}
			// Skip <script> body text cleanly until </script> to avoid JS literal noise.
			closeIdx := indexFoldTagClose(data[i:], "</script")
			if closeIdx < 0 {
				return
			}
			i += closeIdx
		case "style":
			if styleVal, ok := attrs["style"]; ok {
				c.scanCSSURLs([]byte(styleVal), effectiveBase)
			}
			closeStart, closeEnd := indexFoldTagRange(data[i:], "</style")
			if closeStart < 0 {
				c.scanCSSURLs(data[i:], effectiveBase)
				return
			}
			c.scanCSSURLs(data[i:i+closeStart], effectiveBase)
			i += closeEnd
		case "link":
			relSrc := mapRelToSource(attrs["rel"])
			if relSrc != "" {
				if href, ok := attrs["href"]; ok {
					c.addURLRef(href, effectiveBase, relSrc)
				}
				if imgs, ok := attrs["imagesrcset"]; ok {
					for _, u := range parseSrcset(imgs) {
						c.addURLRef(u, effectiveBase, "image")
					}
				}
			}
		case "img":
			for _, key := range []string{"src", "data-src"} {
				if val, ok := attrs[key]; ok {
					c.addURLRef(val, effectiveBase, "image")
				}
			}
			for _, key := range []string{"srcset", "data-srcset"} {
				if val, ok := attrs[key]; ok {
					for _, u := range parseSrcset(val) {
						c.addURLRef(u, effectiveBase, "image")
					}
				}
			}
		case "iframe":
			if src, ok := attrs["src"]; ok {
				c.addURLRef(src, effectiveBase, "iframe")
			}
		case "source":
			if src, ok := attrs["src"]; ok {
				c.addURLRef(src, effectiveBase, "media")
			}
			if srcset, ok := attrs["srcset"]; ok {
				for _, u := range parseSrcset(srcset) {
					c.addURLRef(u, effectiveBase, "image")
				}
			}
		case "video":
			for _, key := range []string{"src", "poster"} {
				if val, ok := attrs[key]; ok {
					c.addURLRef(val, effectiveBase, "media")
				}
			}
		case "audio":
			if val, ok := attrs["src"]; ok {
				c.addURLRef(val, effectiveBase, "media")
			}
		case "embed":
			if val, ok := attrs["src"]; ok {
				c.addURLRef(val, effectiveBase, "embed")
			}
		case "object":
			if val, ok := attrs["data"]; ok {
				c.addURLRef(val, effectiveBase, "embed")
			}
		case "meta":
			if strings.EqualFold(strings.TrimSpace(attrs["http-equiv"]), "content-security-policy") {
				if content, ok := attrs["content"]; ok {
					c.addCSP(html.UnescapeString(content))
				}
			}
		}

		if tagName != "script" && tagName != "style" {
			if styleVal, ok := attrs["style"]; ok {
				c.scanCSSURLs([]byte(styleVal), effectiveBase)
			}
		}
	}
}

func resolveBaseURL(href string, current *url.URL) *url.URL {
	href = strings.TrimSpace(html.UnescapeString(href))
	if href == "" {
		return nil
	}
	if strings.HasPrefix(href, "//") && current != nil && current.Scheme != "" {
		href = current.Scheme + ":" + href
	}
	u, err := url.Parse(href)
	if err != nil {
		return nil
	}
	if current != nil {
		u = current.ResolveReference(u)
	}
	if u.Scheme != "http" && u.Scheme != "https" || u.Hostname() == "" {
		return nil
	}
	return u
}

func parseOpeningTag(data []byte, start int) (tagName string, attrs map[string]string, nextIdx int) {
	n := len(data)
	p := start + 1
	nameStart := p
	for p < n && !isHTMLSpace(data[p]) && data[p] != '>' && data[p] != '/' {
		p++
	}
	if p == nameStart {
		return "", nil, start + 1
	}
	tagName = strings.ToLower(string(data[nameStart:p]))

	for p < n {
		for p < n && isHTMLSpace(data[p]) {
			p++
		}
		if p >= n {
			return tagName, attrs, n
		}
		if data[p] == '>' {
			return tagName, attrs, p + 1
		}
		if data[p] == '/' {
			if p+1 < n && data[p+1] == '>' {
				return tagName, attrs, p + 2
			}
			p++
			continue
		}

		attrStart := p
		for p < n && !isHTMLSpace(data[p]) && data[p] != '=' && data[p] != '>' && data[p] != '/' {
			p++
		}
		if p == attrStart {
			p++
			continue
		}
		attrName := strings.ToLower(string(data[attrStart:p]))

		for p < n && isHTMLSpace(data[p]) {
			p++
		}
		attrVal := ""
		if p < n && data[p] == '=' {
			p++
			for p < n && isHTMLSpace(data[p]) {
				p++
			}
			if p < n && (data[p] == '"' || data[p] == '\'') {
				quote := data[p]
				p++
				valStart := p
				for p < n && data[p] != quote {
					p++
				}
				attrVal = string(data[valStart:p])
				if p < n && data[p] == quote {
					p++
				}
			} else {
				valStart := p
				for p < n && !isHTMLSpace(data[p]) && data[p] != '>' {
					p++
				}
				attrVal = string(data[valStart:p])
			}
		}

		if attrs == nil {
			attrs = make(map[string]string)
		}
		if _, exists := attrs[attrName]; !exists {
			attrs[attrName] = attrVal
		}
	}

	return tagName, attrs, n
}

func indexFoldTagClose(data []byte, closePrefix string) int {
	_, end := indexFoldTagRange(data, closePrefix)
	return end
}

func indexFoldTagRange(data []byte, closePrefix string) (start int, end int) {
	prefixBytes := []byte(closePrefix)
	plen := len(prefixBytes)
	for i := 0; i+plen < len(data); i++ {
		if data[i] != '<' || data[i+1] != '/' {
			continue
		}
		if bytes.EqualFold(data[i:i+plen], prefixBytes) {
			after := data[i+plen]
			if after == '>' || isHTMLSpace(after) || after == '/' {
				gt := bytes.IndexByte(data[i+plen:], '>')
				if gt < 0 {
					return i, len(data)
				}
				return i, i + plen + gt + 1
			}
		}
	}
	return -1, -1
}

func (c *resourceCollector) scanCSSURLs(css []byte, base *url.URL) {
	cssStr := html.UnescapeString(string(css))
	lower := strings.ToLower(cssStr)

	// Scan url(...)
	pos := 0
	for {
		idx := strings.Index(lower[pos:], "url(")
		if idx < 0 {
			break
		}
		// Ensure identifier boundary before "url("
		absIdx := pos + idx
		if absIdx > 0 && isCSSIdentChar(lower[absIdx-1]) {
			pos = absIdx + 4
			continue
		}
		start := absIdx + 4
		j := start
		for j < len(cssStr) && isHTMLSpace(cssStr[j]) {
			j++
		}
		var rawURL string
		var nextPos int
		if j < len(cssStr) && (cssStr[j] == '"' || cssStr[j] == '\'') {
			q := cssStr[j]
			j++
			endQ := strings.IndexByte(cssStr[j:], q)
			if endQ < 0 {
				break
			}
			rawURL = strings.TrimSpace(cssStr[j : j+endQ])
			endParen := strings.IndexByte(cssStr[j+endQ+1:], ')')
			if endParen < 0 {
				nextPos = j + endQ + 1
			} else {
				nextPos = j + endQ + 1 + endParen + 1
			}
		} else {
			endRel := strings.IndexByte(cssStr[start:], ')')
			if endRel < 0 {
				break
			}
			rawURL = strings.TrimSpace(cssStr[start : start+endRel])
			nextPos = start + endRel + 1
		}
		c.addURLRef(rawURL, base, "style")
		pos = nextPos
	}

	// Scan @import "..." / @import '...'
	pos = 0
	for {
		idx := strings.Index(lower[pos:], "@import")
		if idx < 0 {
			break
		}
		restStart := pos + idx + len("@import")
		j := restStart
		for j < len(cssStr) && isHTMLSpace(cssStr[j]) {
			j++
		}
		if j < len(cssStr) && (cssStr[j] == '"' || cssStr[j] == '\'') {
			q := cssStr[j]
			j++
			endQ := strings.IndexByte(cssStr[j:], q)
			if endQ >= 0 {
				c.addURLRef(cssStr[j:j+endQ], base, "style")
				pos = j + endQ + 1
				continue
			}
		}
		pos = restStart
	}
}

func parseSrcset(raw string) []string {
	raw = strings.TrimSpace(html.UnescapeString(raw))
	if raw == "" {
		return nil
	}

	var urls []string
	start := 0
	n := len(raw)

	flush := func(seg string) {
		fields := strings.Fields(strings.TrimSpace(seg))
		if len(fields) > 0 && fields[0] != "" {
			urls = append(urls, fields[0])
		}
	}

	for i := 0; i < n; i++ {
		if raw[i] != ',' {
			continue
		}
		seg := strings.TrimSpace(raw[start:i])
		rest := raw[i+1:]
		isBoundary := len(rest) == 0 ||
			isHTMLSpace(rest[0]) ||
			strings.HasPrefix(rest, "http://") ||
			strings.HasPrefix(rest, "https://") ||
			strings.HasPrefix(rest, "//") ||
			strings.HasPrefix(rest, "/") ||
			hasSrcsetDescriptor(seg)
		if isBoundary {
			flush(seg)
			start = i + 1
		}
	}
	if start < n {
		flush(raw[start:])
	}
	return urls
}

func hasSrcsetDescriptor(seg string) bool {
	fields := strings.Fields(seg)
	if len(fields) < 2 {
		return false
	}
	last := strings.ToLower(fields[len(fields)-1])
	if len(last) < 2 {
		return false
	}
	suffix := last[len(last)-1]
	return suffix == 'w' || suffix == 'x'
}

func splitHTTPHeaderEntries(s string) []string {
	var parts []string
	inAngles := false
	inQuotes := false
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			if !inAngles {
				inQuotes = !inQuotes
			}
		case '<':
			if !inQuotes {
				inAngles = true
			}
		case '>':
			if !inQuotes {
				inAngles = false
			}
		case ',':
			if !inAngles && !inQuotes {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, s[start:])
	return parts
}

func stripMatchingQuotes(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'')) {
		return strings.TrimSpace(s[1 : len(s)-1])
	}
	return s
}

func normalizeExtractedHost(raw string) string {
	h := strings.ToLower(strings.TrimSpace(raw))
	h = strings.TrimSuffix(h, ".")
	h = strings.Trim(h, "[]")
	return h
}

func isAcceptableHostname(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".local") ||
		strings.HasSuffix(host, ".lan") ||
		strings.HasSuffix(host, ".internal") ||
		strings.HasSuffix(host, ".home.arpa") {
		return false
	}
	if !strings.Contains(host, ".") {
		return false
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 {
			return false
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			b := label[i]
			isAZ := b >= 'a' && b <= 'z'
			isDigit := b >= '0' && b <= '9'
			if !isAZ && !isDigit && b != '-' {
				return false
			}
		}
	}
	// Reject purely numeric last label (malformed IPv4-like token)
	tld := labels[len(labels)-1]
	allDigits := true
	for i := 0; i < len(tld); i++ {
		if tld[i] < '0' || tld[i] > '9' {
			allDigits = false
			break
		}
	}
	return !allDigits
}

func isHTMLSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r' || b == '\n' || b == '\f'
}

func isCSSIdentChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '-' || b == '_'
}
