package captcha

import (
	"net/http"
	"net/url"
	"strings"
)

// shouldDelegateGenericProxy reports whether the request should be
// forwarded to freeturn's own :8765/generic_proxy (VK OAuth HTML rewriting).
func shouldDelegateGenericProxy(proxyURL string) bool {
	host := hostnameFromProxyURL(proxyURL)
	if host == "" {
		return false
	}
	if needsExtendedGenericProxyHost(host) {
		return false
	}
	return isAllowedGenericHost(host)
}

func hostnameFromProxyURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// needsExtendedGenericProxyHost reports hosts blocked by freeturn :8765 generic_proxy
// but required for VK Smart Captcha (sdk-api.appteka.ru, etc.).
func needsExtendedGenericProxyHost(hostname string) bool {
	host := strings.ToLower(hostname)
	for _, suffix := range extendedGenericProxyHosts {
		s := strings.TrimPrefix(suffix, ".")
		if host == s || strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

var extendedGenericProxyHosts = []string{
	".appteka.ru",
	".apptracer.ru",
}

func rewriteLocalURLsPreserveRedirectURI(s, base string) string {
	return rewriteLocalURLsMaybePreserveRedirectURI(s, base, true)
}

func rewriteLocalURLsMaybePreserveRedirectURI(s, base string, preserveRedirectURI bool) string {
	if !preserveRedirectURI {
		base = strings.TrimSuffix(base, "/")
		for _, origin := range localOrigins {
			s = strings.ReplaceAll(s, origin, base)
		}
		for _, origin := range localOrigins {
			enc := url.QueryEscape(origin)
			s = strings.ReplaceAll(s, enc, url.QueryEscape(base))
		}
		return s
	}

	var out strings.Builder
	lower := strings.ToLower(s)
	i := 0
	for {
		idx := strings.Index(lower[i:], "redirect_uri=")
		if idx < 0 {
			out.WriteString(rewriteLocalURLsMaybePreserveRedirectURI(s[i:], base, false))
			break
		}
		abs := i + idx
		out.WriteString(rewriteLocalURLsMaybePreserveRedirectURI(s[i:abs], base, false))
		j := abs + len("redirect_uri=")
		for j < len(s) && s[j] != '&' && s[j] != '"' && s[j] != '\'' {
			j++
		}
		out.WriteString(s[abs:j])
		i = j
	}
	return out.String()
}

func shouldRewriteContentType(ct string) bool {
	ct = strings.ToLower(ct)
	return strings.Contains(ct, "text/html") ||
		strings.Contains(ct, "application/xhtml+xml") ||
		strings.Contains(ct, "javascript") ||
		strings.Contains(ct, "application/json") ||
		strings.Contains(ct, "text/plain")
}

func shouldInjectBaseTag(html string) bool {
	if strings.Contains(html, "__ftpCaptcha") ||
		strings.Contains(html, "not_robot_captcha") ||
		strings.Contains(html, "local-captcha-result") {
		return true
	}
	lower := strings.ToLower(html)
	return !strings.Contains(lower, "id.vk.ru") &&
		!strings.Contains(lower, "id.vk.com") &&
		!strings.Contains(lower, "oauth.vk.com")
}

func rewriteResponseHeaders(h http.Header, base string) {
	if loc := h.Get("Location"); loc != "" {
		if rewritten := rewriteURL(loc, base); rewritten != loc {
			h.Set("Location", rewritten)
		}
	}
}
