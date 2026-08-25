package captcha

import (
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
)

// Captcha proxy hosts allowed when awg-manager handles /generic_proxy directly.
var genericProxyHosts = []string{
	".vk.com", ".vk.ru", ".vkontakte.ru",
	".userapi.com", ".okcdn.ru", ".mycdn.me",
	".api.vk.com", ".api.vk.ru",
	".id.vk.ru", ".id.vk.com",
	".appteka.ru", ".apptracer.ru",
	".vkuser.net", ".vk-cdn.net",
}

// serveGenericProxy handles GET/POST .../captcha/generic_proxy?proxy_url=...
// For VK OAuth hosts it delegates to freeturn's own :8765 generic_proxy so HTML
// rewriting and login redirects stay in the freeturn funnel. Extended allowlist
// hosts (appteka.ru) are fetched directly here.
func serveGenericProxy(w http.ResponseWriter, r *http.Request, proxyBase string) {
	targetRaw := r.URL.Query().Get("proxy_url")
	target, err := url.Parse(targetRaw)
	if err != nil || target.Host == "" || target.Scheme == "" {
		http.Error(w, "Bad URL", http.StatusBadRequest)
		return
	}
	if !isAllowedGenericHost(target.Hostname()) {
		http.Error(w, "Forbidden host", http.StatusForbidden)
		return
	}

	proxy := &httputil.ReverseProxy{
		Transport: http.DefaultTransport,
		Rewrite: func(req *httputil.ProxyRequest) {
			out := req.Out
			out.URL = target
			out.Host = target.Host
			rewriteGenericProxyHeaders(out, target, proxyBase)
		},
		ModifyResponse: func(res *http.Response) error {
			stripGenericProxyResponseHeaders(res.Header)
			rewriteResponseHeaders(res.Header, proxyBase)

			ct := res.Header.Get("Content-Type")
			if !shouldRewriteContentType(ct) {
				return nil
			}
			body, err := io.ReadAll(res.Body)
			if err != nil {
				return err
			}
			_ = res.Body.Close()
			rewritten := rewriteLocalURLsPreserveRedirectURI(string(body), proxyBase)
			rewritten = injectNavShim(rewritten, proxyBase)
			rewritten = injectCryptoShim(rewritten)
			res.Body = io.NopCloser(strings.NewReader(rewritten))
			res.ContentLength = int64(len(rewritten))
			res.Header.Set("Content-Length", strconv.Itoa(len(rewritten)))
			res.Header.Del("Content-Encoding")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			http.Error(w, "captcha proxy error: "+err.Error(), http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
}

func isAllowedGenericHost(hostname string) bool {
	host := strings.ToLower(hostname)
	for _, suffix := range genericProxyHosts {
		s := strings.TrimPrefix(suffix, ".")
		if host == s || strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

func rewriteGenericProxyHeaders(req *http.Request, target *url.URL, proxyBase string) {
	targetOrigin := target.Scheme + "://" + target.Host
	base := strings.TrimSuffix(proxyBase, "/")

	for _, h := range []string{
		"Accept-Encoding",
		"X-Requested-With",
		"X-Android-Package",
		"X-Android-Cert",
	} {
		req.Header.Del(h)
	}

	for _, headerName := range []string{"Origin", "Referer"} {
		val := req.Header.Get(headerName)
		if val == "" {
			continue
		}
		if strings.HasPrefix(val, base) || strings.HasPrefix(val, upstreamOrigin) ||
			strings.HasPrefix(val, "http://localhost:") || strings.HasPrefix(val, "http://127.0.0.1:") {
			if headerName == "Origin" {
				req.Header.Set("Origin", targetOrigin)
			} else {
				req.Header.Set("Referer", targetOrigin+"/")
			}
			continue
		}
	}
	if o := req.Header.Get("Origin"); o != "" && !strings.EqualFold(o, targetOrigin) {
		// Browser may still send awg-manager origin on XHR; VK expects target origin.
		if strings.Contains(o, instancesPath) {
			req.Header.Set("Origin", targetOrigin)
		}
	}
}

func stripGenericProxyResponseHeaders(h http.Header) {
	for _, name := range []string{
		"Content-Security-Policy",
		"Content-Security-Policy-Report-Only",
		"X-Content-Security-Policy",
		"X-WebKit-CSP",
		"Cross-Origin-Opener-Policy",
		"Cross-Origin-Embedder-Policy",
		"Cross-Origin-Resource-Policy",
		"X-Frame-Options",
		"Strict-Transport-Security",
	} {
		h.Del(name)
	}
	h.Set("Access-Control-Allow-Origin", "*")
	normalizeSetCookies(h)
}

func normalizeSetCookies(h http.Header) {
	cookies := (&http.Response{Header: h}).Cookies()
	if len(cookies) == 0 {
		return
	}
	h.Del("Set-Cookie")
	for _, c := range cookies {
		c.Domain = ""
		c.Secure = false
		if c.SameSite == http.SameSiteNoneMode || c.SameSite == http.SameSiteStrictMode {
			c.SameSite = http.SameSiteLaxMode
		}
		h.Add("Set-Cookie", c.String())
	}
}
