package captcha

import (
	"compress/gzip"
	_ "embed"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
)

//go:embed crypto_shim.js
var cryptoShimJS string

const upstreamOrigin = "http://127.0.0.1:8765"

var localOrigins = []string{
	"http://localhost:8765",
	"http://127.0.0.1:8765",
	"http://[::1]:8765",
}

// newReverseProxy forwards browser traffic to freeturn's local captcha
// server and rewrites localhost:8765 URLs to the awg-manager proxy base.
func newReverseProxy(proxyBase string, targetPath string) *httputil.ReverseProxy {
	upstream, _ := url.Parse(upstreamOrigin)
	base := strings.TrimSuffix(proxyBase, "/")

	proxy := httputil.NewSingleHostReverseProxy(upstream)
	origDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		origDirector(req)
		inQuery := req.URL.RawQuery
		req.URL.Path = targetPath
		req.URL.RawPath = ""
		req.URL.RawQuery = inQuery
		req.Host = upstream.Host
		// freeturn rewrites Origin/Referer only from localhost:8765 to VK; without
		// this shim the browser sends awg-manager URLs and VK rejects captcha checks.
		rewriteOutboundHeaders(req, base)
	}
	proxy.ModifyResponse = func(res *http.Response) error {
		rewriteResponseHeaders(res.Header, base)
		stripGenericProxyResponseHeaders(res.Header)
		normalizeSetCookies(res.Header)

		ct := res.Header.Get("Content-Type")
		if !shouldRewriteContentType(ct) {
			return nil
		}

		body, err := readMaybeGzip(res.Body, res.Header.Get("Content-Encoding"))
		if err != nil {
			return err
		}
		if err := res.Body.Close(); err != nil {
			return err
		}

		rewritten := rewriteBody(string(body), base)
		rewritten = injectPopupHelper(rewritten)
		res.Body = io.NopCloser(strings.NewReader(rewritten))
		res.ContentLength = int64(len(rewritten))
		res.Header.Set("Content-Length", strconv.Itoa(len(rewritten)))
		res.Header.Del("Content-Encoding")
		return nil
	}
	return proxy
}

// rewriteOutboundHeaders maps browser Origin/Referer from the awg-manager
// proxy URL back to 127.0.0.1:8765 before forwarding to freeturn-client.
func rewriteOutboundHeaders(req *http.Request, proxyBase string) {
	base := strings.TrimSuffix(proxyBase, "/")
	local := upstreamOrigin

	if origin := req.Header.Get("Origin"); origin != "" {
		if strings.EqualFold(origin, base) || strings.HasPrefix(origin, base+"/") {
			req.Header.Set("Origin", local)
		}
	}
	if ref := req.Header.Get("Referer"); ref != "" {
		if strings.HasPrefix(ref, base) {
			suffix := strings.TrimPrefix(ref, base)
			if suffix == "" {
				suffix = "/"
			}
			req.Header.Set("Referer", local+suffix)
		}
	}
}

func rewriteURL(raw, base string) string {
	base = strings.TrimSuffix(base, "/")
	for _, origin := range localOrigins {
		if strings.HasPrefix(raw, origin) {
			return base + strings.TrimPrefix(raw, origin)
		}
	}
	if strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") {
		return base + raw
	}
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" {
		if isVKOAuthNavigationHost(u.Hostname()) && !strings.Contains(raw, "/generic_proxy?proxy_url=") {
			return base + "/generic_proxy?proxy_url=" + url.QueryEscape(raw)
		}
	}
	return raw
}

func isVKOAuthNavigationHost(host string) bool {
	switch strings.ToLower(host) {
	case "id.vk.ru", "id.vk.com", "oauth.vk.com", "oauth.vk.ru":
		return true
	default:
		return false
	}
}

func rewriteBody(body, base string) string {
	body = rewriteLocalURLsPreserveRedirectURI(body, base)
	replacements := [][2]string{
		{`"/generic_proxy`, `"` + base + `/generic_proxy`},
		{`'/generic_proxy`, `'` + base + `/generic_proxy`},
		{`"/local-captcha-result`, `"` + base + `/local-captcha-result`},
		{`'/local-captcha-result`, `'` + base + `/local-captcha-result`},
	}
	for _, pair := range replacements {
		body = strings.ReplaceAll(body, pair[0], pair[1])
	}
	if shouldInjectBaseTag(body) {
		body = injectBaseTag(body, base+"/")
	}
	body = injectNavShim(body, base)
	return injectCryptoShim(body)
}

func injectNavShim(html, base string) string {
	if !strings.Contains(strings.ToLower(html), "<html") &&
		!strings.Contains(strings.ToLower(html), "<head") {
		return html
	}
	escaped := strings.ReplaceAll(base, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	shim := `<script>(function(){var b="` + escaped + `";var l=["http://127.0.0.1:8765","http://localhost:8765","http://[::1]:8765"];` +
		`function isVK(u){return/^https:\\/\\/([^\\/]+\\.)?(id\\.vk\\.(ru|com)|oauth\\.vk\\.(com|ru))\\/?/i.test(u);}` +
		`function rw(u){var s=String(u||"");for(var i=0;i<l.length;i++){if(s.indexOf(l[i])===0)return b+s.slice(l[i].length);}` +
		`if(s.indexOf("/")===0&&s.indexOf("//")!==0)return b+s;` +
		`if(isVK(s)&&s.indexOf("/generic_proxy?")<0)return b+"/generic_proxy?proxy_url="+encodeURIComponent(s);return s;}` +
		`try{var a=Location.prototype.assign,r=Location.prototype.replace;` +
		`Location.prototype.assign=function(u){return a.call(this,rw(u));};` +
		`Location.prototype.replace=function(u){return r.call(this,rw(u));};` +
		`var d=Object.getOwnPropertyDescriptor(Location.prototype,"href");` +
		`if(d&&d.set){Object.defineProperty(Location.prototype,"href",{get:d.get,set:function(v){d.set.call(this,rw(v));},configurable:true,enumerable:true});}}catch(e){}` +
		`var op=window.open;window.open=function(u,n,f){return op.call(window,u?rw(u):u,n,f);};` +
		`document.addEventListener("click",function(e){var el=e.target&&e.target.closest?e.target.closest("a"):null;if(!el||!el.href)return;var n=rw(el.href);if(n!==el.href){e.preventDefault();location.assign(n);}},true);` +
		`document.addEventListener("submit",function(e){var f=e.target;if(!f||!f.action)return;var n=rw(f.action);if(n!==f.action)f.action=n;},true);})();</script>`
	lower := strings.ToLower(html)
	if strings.Contains(lower, "<head>") {
		return strings.Replace(html, "<head>", "<head>"+shim, 1)
	}
	if strings.Contains(lower, "</body>") {
		return strings.Replace(html, "</body>", shim+"</body>", 1)
	}
	return shim + html
}

func injectBaseTag(html, baseHref string) string {
	tag := `<base href="` + baseHref + `">`
	lower := strings.ToLower(html)
	switch {
	case strings.Contains(lower, "<head>"):
		return strings.Replace(html, "<head>", "<head>"+tag, 1)
	case strings.Contains(lower, "<head "):
		if idx := strings.Index(lower, "<head"); idx >= 0 {
			if end := strings.Index(html[idx:], ">"); end >= 0 {
				pos := idx + end + 1
				return html[:pos] + tag + html[pos:]
			}
		}
	case strings.Contains(lower, "<html>"):
		return strings.Replace(html, "<html>", "<html>"+tag, 1)
	}
	// Headless fragment: parsers hoist leading <base> into the implied <head>.
	return tag + html
}

// injectPopupHelper adds a banner on the freeturn success page and tries to
// close popup windows opened from awg-manager after captcha is accepted.
func injectPopupHelper(html string) string {
	helper := `<script>(function(){` +
		`if(location.pathname.indexOf("local-captcha-result")<0)return;` +
		`var b=document.createElement("div");` +
		`b.style.cssText="margin:12px;padding:12px 14px;border-radius:8px;background:#1a472a;color:#e8f5e9;font:14px/1.4 sans-serif;text-align:center";` +
		`b.textContent="Капча принята. Можно закрыть это окно — freeturn продолжит работу.";` +
		`document.body?document.body.insertBefore(b,document.body.firstChild):document.documentElement.appendChild(b);` +
		`setTimeout(function(){try{if(window.opener)window.close()}catch(e){}},2500);` +
		`})();</script>`
	if strings.Contains(strings.ToLower(html), "</body>") {
		return strings.Replace(html, "</body>", helper+"</body>", 1)
	}
	return html + helper
}

func injectCryptoShim(html string) string {
	tag := "<script>" + cryptoShimJS + "</script>"
	lower := strings.ToLower(html)
	switch {
	case strings.Contains(lower, "<head>"):
		return strings.Replace(html, "<head>", "<head>"+tag, 1)
	case strings.Contains(lower, "<head "):
		if idx := strings.Index(lower, "<head"); idx >= 0 {
			if end := strings.Index(html[idx:], ">"); end >= 0 {
				pos := idx + end + 1
				return html[:pos] + tag + html[pos:]
			}
		}
	case strings.Contains(lower, "<html>"):
		return strings.Replace(html, "<html>", "<html>"+tag, 1)
	}
	return tag + html
}

func readMaybeGzip(r io.Reader, encoding string) ([]byte, error) {
	if encoding != "gzip" {
		return io.ReadAll(r)
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		return io.ReadAll(r)
	}
	defer gz.Close()
	return io.ReadAll(gz)
}
