package manual

import (
	"bytes"
	"compress/gzip"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	neturl "net/url"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/samosvalishe/free-turn-proxy/internal/client/ish"
	"github.com/samosvalishe/free-turn-proxy/internal/logx"
)

// Debug включает логирование проксируемого браузерного трафика.
var Debug bool

var Log logx.Logger = logx.Nop()

func SetLogger(l logx.Logger) { Log = logx.OrNop(l) }

const captchaListenPort = "8765"

//go:embed inject.js
var injectJS string

// injectScriptTag: конфиг отдельной строкой (json.Marshal экранирует, включая "<"),
// сам inject.js статичный, без плейсхолдеров.
func injectScriptTag(localOrigin, upstreamOrigin string) string {
	cfg, _ := json.Marshal(map[string]string{"local": localOrigin, "upstream": upstreamOrigin})
	return "\n<script>window.__ftpCaptcha=" + string(cfg) + ";</script>\n<script>\n" + injectJS + "</script>\n"
}

type browserCommand struct {
	name string
	args []string
}

func localCaptchaOrigin() string {
	return "http://localhost:" + captchaListenPort
}

func localCaptchaListenAddrs() []string {
	return []string{
		"127.0.0.1:" + captchaListenPort,
		"[::1]:" + captchaListenPort,
	}
}

func localCaptchaHosts() []string {
	return []string{
		"localhost:" + captchaListenPort,
		"127.0.0.1:" + captchaListenPort,
		"[::1]:" + captchaListenPort,
	}
}

func isAllowedProxyHost(hostname string) bool {
	allowed := []string{
		".vk.com", ".vk.ru", ".vkontakte.ru",
		".userapi.com", ".okcdn.ru", ".mycdn.me",
		".api.vk.com", ".api.vk.ru",
	}
	for _, suffix := range allowed {
		if strings.HasSuffix(hostname, suffix) || hostname == suffix[1:] {
			return true
		}
	}
	return false
}

func isLocalCaptchaHost(host string) bool {
	for _, localHost := range localCaptchaHosts() {
		if strings.EqualFold(host, localHost) {
			return true
		}
	}
	return false
}

func localCaptchaURLForTarget(targetURL *neturl.URL) string {
	localURL := &neturl.URL{
		Scheme:   "http",
		Host:     "localhost:" + captchaListenPort,
		Path:     targetURL.Path,
		RawPath:  targetURL.RawPath,
		RawQuery: targetURL.RawQuery,
	}
	if localURL.Path == "" {
		localURL.Path = "/"
	}
	return localURL.String()
}

func targetOrigin(targetURL *neturl.URL) string {
	return targetURL.Scheme + "://" + targetURL.Host
}

func isSafeLocalRedirectPath(raw string) bool {
	if raw == "" || raw[0] != '/' {
		return false
	}
	if len(raw) > 1 && (raw[1] == '/' || raw[1] == '\\') {
		return false
	}
	return true
}

func rewriteProxyRedirectLocation(raw string, targetURL *neturl.URL) (string, bool) {
	if isSafeLocalRedirectPath(raw) {
		return raw, true
	}

	parsed, err := neturl.Parse(raw)
	if err != nil {
		return "", false
	}
	if !strings.EqualFold(parsed.Scheme, targetURL.Scheme) || !strings.EqualFold(parsed.Host, targetURL.Host) {
		return "", false
	}

	return localCaptchaURLForTarget(parsed), true
}

func rewriteProxyHeaderURL(raw string, targetURL *neturl.URL) string {
	if raw == "" {
		return raw
	}
	parsed, err := neturl.Parse(raw)
	if err != nil {
		return raw
	}
	if parsed.Scheme != "http" || !isLocalCaptchaHost(parsed.Host) {
		return raw
	}
	parsed.Scheme = targetURL.Scheme
	parsed.Host = targetURL.Host
	return parsed.String()
}

func rewriteProxyRequest(req *http.Request, targetURL *neturl.URL) {
	req.URL.Scheme = targetURL.Scheme
	req.URL.Host = targetURL.Host
	if req.URL.Path == "" {
		req.URL.Path = targetURL.Path
	}
	req.Host = targetURL.Host

	req.Header.Del("Accept-Encoding")
	req.Header.Del("TE") // отключить transfer encoding compression
	for _, h := range []string{
		"X-Requested-With",
		"X-Android-Package",
		"X-Android-Cert",
		"X-Client-Data",
		"X-Discord-Locale",
		"X-Discord-Timezone",
		"Save-Data",
		"Purpose",
		"Sec-Purpose",
	} {
		req.Header.Del(h)
	}
	for _, headerName := range []string{"Origin", "Referer"} {
		if rewritten := rewriteProxyHeaderURL(req.Header.Get(headerName), targetURL); rewritten != "" {
			req.Header.Set(headerName, rewritten)
		} else {
			req.Header.Del(headerName)
		}
	}
}

func extractSuccessToken(body []byte) string {
	var payload struct {
		Response struct {
			SuccessToken string `json:"success_token"`
		} `json:"response"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	return payload.Response.SuccessToken
}

func rewriteProxyCookies(header http.Header) {
	cookies := (&http.Response{Header: header}).Cookies()
	if len(cookies) == 0 {
		return
	}
	header.Del("Set-Cookie")
	for _, cookie := range cookies {
		cookie.Domain = ""
		cookie.Secure = false
		cookie.Partitioned = false
		if cookie.SameSite == http.SameSiteNoneMode || cookie.SameSite == http.SameSiteStrictMode {
			cookie.SameSite = http.SameSiteLaxMode
		}
		header.Add("Set-Cookie", cookie.String())
	}
}

var htmlURLAttrDoubleRe = regexp.MustCompile(`(?i)((?:src|href|action)\s*=\s*)"((?:https?:)?//[^"]+)"`)
var htmlURLAttrSingleRe = regexp.MustCompile(`(?i)((?:src|href|action)\s*=\s*)'((?:https?:)?//[^']+)'`)
var htmlScriptContentRe = regexp.MustCompile(`(?is)(<script[^>]*>)(.*?)(</script>)`)
var htmlStyleContentRe = regexp.MustCompile(`(?is)(<style[^>]*>)(.*?)(</style>)`)

// rewriteHTMLAttrsServerSide переписывает абсолютные и protocol-relative URL
// в src/href/action HTML на стороне сервера. URL, совпадающие с upstream origin,
// идут на localhost; остальные - через /generic_proxy, чтобы cross-domain
// ресурсы (st.vk.ru, userapi.com и т.д.) грузились через прокси.
func rewriteHTMLAttrsServerSide(html string, targetURL *neturl.URL) string {
	localOrigin := localCaptchaOrigin()
	upstreamOrigin := targetOrigin(targetURL)

	rewriteURL := func(rawURL string) string {
		// нормализовать protocol-relative URL в абсолютный по upstream scheme
		absURL := rawURL
		if strings.HasPrefix(rawURL, "//") {
			absURL = targetURL.Scheme + ":" + rawURL
		}
		if strings.HasPrefix(absURL, upstreamOrigin) {
			return localOrigin + absURL[len(upstreamOrigin):]
		}
		if strings.HasPrefix(absURL, localOrigin) {
			return rawURL
		}
		// прочие абсолютные URL -> через generic_proxy
		return "/generic_proxy?proxy_url=" + neturl.QueryEscape(absURL)
	}

	var placeholders = make(map[string]string)

	html = htmlScriptContentRe.ReplaceAllStringFunc(html, func(match string) string {
		groups := htmlScriptContentRe.FindStringSubmatch(match)
		if len(groups) < 4 {
			return match
		}
		id := fmt.Sprintf("@@CONTENT_%d@@", len(placeholders))
		placeholders[id] = groups[2]
		return groups[1] + id + groups[3]
	})

	html = htmlStyleContentRe.ReplaceAllStringFunc(html, func(match string) string {
		groups := htmlStyleContentRe.FindStringSubmatch(match)
		if len(groups) < 4 {
			return match
		}
		id := fmt.Sprintf("@@CONTENT_%d@@", len(placeholders))
		placeholders[id] = groups[2]
		return groups[1] + id + groups[3]
	})

	html = htmlURLAttrDoubleRe.ReplaceAllStringFunc(html, func(match string) string {
		groups := htmlURLAttrDoubleRe.FindStringSubmatch(match)
		if len(groups) < 3 {
			return match
		}
		return groups[1] + `"` + rewriteURL(groups[2]) + `"`
	})

	html = htmlURLAttrSingleRe.ReplaceAllStringFunc(html, func(match string) string {
		groups := htmlURLAttrSingleRe.FindStringSubmatch(match)
		if len(groups) < 3 {
			return match
		}
		return groups[1] + `'` + rewriteURL(groups[2]) + `'`
	})

	for id, content := range placeholders {
		html = strings.Replace(html, id, content, 1)
	}

	return html
}

func rewriteCaptchaHTML(html string, targetURL *neturl.URL) string {
	localOrigin := localCaptchaOrigin()
	upstreamOrigin := targetOrigin(targetURL)

	// Шаг 1: текстовая замена основного upstream origin
	html = strings.ReplaceAll(html, upstreamOrigin, localOrigin)

	// Шаг 2: серверный rewrite остальных абсолютных URL в HTML-атрибутах.
	// Критично: браузер начинает грузить <script src> / <link href> / <img src>
	// сразу при парсинге HTML - раньше любых инжектированных JS-перехватов.
	html = rewriteHTMLAttrsServerSide(html, targetURL)

	script := injectScriptTag(localOrigin, upstreamOrigin)

	// Шаг 3: инжектируем клиентский скрипт как можно раньше - после <head>,
	// чтобы XHR/fetch-перехваты были активны до любого inline <script> в <head>.
	switch {
	case strings.Contains(html, "<head>"):
		return strings.Replace(html, "<head>", "<head>"+script, 1)
	case strings.Contains(html, "</head>"):
		return strings.Replace(html, "</head>", script+"</head>", 1)
	case strings.Contains(html, "</body>"):
		return strings.Replace(html, "</body>", script+"</body>", 1)
	default:
		return html + script
	}
}

func newCaptchaProxyTransport(dialer net.Dialer) *http.Transport {
	return &http.Transport{
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     false,
		DialContext:           dialer.DialContext,
	}
}

func startCaptchaServer(srv *http.Server, logPrefix string) error {
	var listenErrs []string
	var listening bool

	for _, addr := range localCaptchaListenAddrs() {
		listener, err := net.Listen("tcp", addr) //nolint:noctx
		if err != nil {
			listenErrs = append(listenErrs, fmt.Sprintf("%s (%v)", addr, err))
			continue
		}
		listening = true
		wrappedListener, err := ish.WrapListener(listener)
		if err != nil {
			Log.Warnf("%s: failed to wrap listener for iSH: %v", logPrefix, err)
			wrappedListener = listener
		}
		go func(listener net.Listener) {
			if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				Log.Errorf("%s: %s", logPrefix, err)
			}
		}(wrappedListener)
	}

	if listening {
		return nil
	}

	return fmt.Errorf("captcha listeners failed: %s", strings.Join(listenErrs, "; "))
}

// runCaptchaServerAndWait открывает браузер и ждёт токен решения.
// При срабатывании ctx возвращает ctx.Err(); в обоих случаях HTTP-сервер останавливается.
func runCaptchaServerAndWait(ctx context.Context, handler http.Handler, captchaURL string, keyCh <-chan string, logPrefix string, present func(string)) (string, error) {
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}

	if err := startCaptchaServer(srv, logPrefix); err != nil {
		return "", err
	}

	defer func() { //nolint:contextcheck // shutdown intentionally uses fresh context after parent is cancelled
		// best-effort shutdown. На iSH SetDeadline - no-op, Shutdown может
		// таймаутить при сливе listener'ов; результат всё равно прокидываем.
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer shutCancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			Log.Warnf("%s: shutdown warning: %v", logPrefix, err)
		}
	}()

	fmt.Println("\n==============================================")
	fmt.Println("ACTION REQUIRED: MANUAL CAPTCHA SOLVING NEEDED")
	fmt.Println("If your browser didn't open automatically,")
	fmt.Println("manually open this URL: " + localCaptchaOrigin())
	fmt.Println("==============================================")
	fmt.Println()

	present(captchaURL)

	select {
	case key := <-keyCh:
		return key, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// notifyKey пушит ключ в канал без блокировки.
func notifyKey(keyCh chan<- string, key string) {
	if key != "" {
		select {
		case keyCh <- key:
		default:
		}
	}
}

type loggingTransport struct {
	rt http.RoundTripper
}

func (t *loggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	isCaptchaRequest := req.Body != nil && (strings.Contains(req.URL.Path, "captchaNotRobot.check") || strings.Contains(req.URL.Path, "captchaNotRobot.componentDone"))

	if isCaptchaRequest {
		b, err := io.ReadAll(req.Body)
		if err != nil {
			Log.Warnf("[Captcha Proxy] failed to read request body: %v", err)
			b = nil
		}
		req.Body = io.NopCloser(bytes.NewReader(b))

		if Debug {
			Log.Debugf("[Captcha Proxy] real browser sent %s data: %s", req.URL.Path, string(b))
			for k, v := range req.Header {
				Log.Debugf("[Captcha Proxy] header (%s): %s = %s", req.URL.Path, k, strings.Join(v, ", "))
			}
		}
	}
	return t.rt.RoundTrip(req)
}

// SolveViaProxy проксирует VK redirect_uri через локальный HTTP-сервер,
// переписывая абсолютные URL так, чтобы браузер всё время оставался на
// 127.0.0.1:8765; возвращает результирующий auth-токен.
func SolveViaProxy(ctx context.Context, redirectURI string, dialer net.Dialer) (string, error) {
	return solveViaProxy(ctx, redirectURI, dialer, openBrowser)
}

// SolveViaProxyWithPresenter передаёт URL вызывающему после запуска сервера.
func SolveViaProxyWithPresenter(ctx context.Context, redirectURI string, dialer net.Dialer, present func(string)) (string, error) {
	if present == nil {
		present = func(string) {}
	}
	return solveViaProxy(ctx, redirectURI, dialer, present)
}

func solveViaProxy(ctx context.Context, redirectURI string, dialer net.Dialer, present func(string)) (string, error) {
	keyCh := make(chan string, 1)

	targetURL, err := neturl.Parse(redirectURI)
	if err != nil {
		return "", fmt.Errorf("invalid redirect URI: %v", err)
	}

	transport := &loggingTransport{rt: newCaptchaProxyTransport(dialer)}

	proxy := &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(req *httputil.ProxyRequest) {
			rewriteProxyRequest(req.Out, targetURL)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			Log.Errorf("[Captcha Proxy] %s %s: %v", r.Method, r.URL.String(), err)
			http.Error(w, "ошибка прокси капчи, детали в консоли клиента", http.StatusBadGateway)
		},
		ModifyResponse: func(res *http.Response) error {
			rewriteProxyCookies(res.Header)

			if res.StatusCode >= 300 && res.StatusCode < 400 {
				if loc := res.Header.Get("Location"); loc != "" {
					// не логируем полный redirect URL - шум в консоли
					if rewritten, ok := rewriteProxyRedirectLocation(loc, targetURL); ok {
						res.Header.Set("Location", rewritten)
					} else {
						res.Header.Del("Location")
					}
				}
			}

			contentType := res.Header.Get("Content-Type")
			contentEncoding := res.Header.Get("Content-Encoding")
			if Debug {
				Log.Debugf("[Captcha Proxy] %s %d | Content-Type: %q, Encoding: %q", res.Request.Method, res.StatusCode, contentType, contentEncoding)
			}

			shouldInspectBody := strings.Contains(contentType, "text/html") ||
				strings.Contains(contentType, "application/xhtml+xml") ||
				strings.Contains(res.Request.URL.Path, "captchaNotRobot.check")

			if !shouldInspectBody {
				return nil
			}

			reader := res.Body
			if res.Header.Get("Content-Encoding") == "gzip" {
				gzReader, err := gzip.NewReader(res.Body)
				if err == nil {
					reader = gzReader
					defer func() {
						if err := gzReader.Close(); err != nil {
							Log.Warnf("[Captcha Proxy] close gzip reader: %v", err)
						}
					}()
				}
			}

			bodyBytes, err := io.ReadAll(reader)
			if err != nil {
				return err
			}
			if err := res.Body.Close(); err != nil {
				return err
			}

			if strings.Contains(res.Request.URL.Path, "captchaNotRobot.check") {
				notifyKey(keyCh, extractSuccessToken(bodyBytes))
			}

			if strings.Contains(contentType, "text/html") {
				for _, headerName := range []string{
					"Content-Security-Policy",
					"Content-Security-Policy-Report-Only",
					"X-Content-Security-Policy",
					"X-WebKit-CSP",
					"Cross-Origin-Opener-Policy",
					"Cross-Origin-Embedder-Policy",
					"Cross-Origin-Resource-Policy",
					"X-Frame-Options",
					"Strict-Transport-Security",
					"Alt-Svc",
				} {
					res.Header.Del(headerName)
				}

				bodyBytes = []byte(rewriteCaptchaHTML(string(bodyBytes), targetURL))
				res.Header.Del("Content-Encoding")
			}

			res.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			res.ContentLength = int64(len(bodyBytes))
			res.Header.Set("Content-Length", fmt.Sprint(len(bodyBytes)))

			return nil
		},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/local-captcha-result", func(w http.ResponseWriter, r *http.Request) {
		token := r.FormValue("token")
		if token != "" {
			Log.Infof("[Captcha] received success token from browser (%d bytes)", len(token))
			notifyKey(keyCh, token)
		} else {
			Log.Warnf("[Captcha] received empty token from browser")
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprint(w, "ok")
	})

	mux.HandleFunc("/generic_proxy", func(w http.ResponseWriter, r *http.Request) {
		targetAuthURL := r.URL.Query().Get("proxy_url")
		targetParsed, err := neturl.Parse(targetAuthURL)
		if err != nil || targetParsed.Host == "" {
			http.Error(w, "Bad URL", http.StatusBadRequest)
			return
		}
		if !isAllowedProxyHost(targetParsed.Hostname()) {
			http.Error(w, "Forbidden host", http.StatusForbidden)
			return
		}
		genericReverse := &httputil.ReverseProxy{
			Transport: transport,
			Rewrite: func(req *httputil.ProxyRequest) {
				req.Out.URL.Path = targetParsed.Path
				req.Out.URL.RawQuery = targetParsed.RawQuery
				rewriteProxyRequest(req.Out, targetParsed)
			},
			ModifyResponse: func(res *http.Response) error {
				// убираем security-заголовки, блокирующие cross-origin загрузку
				// статики (JS/CSS) при проксировании.
				for _, h := range []string{
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
					res.Header.Del(h)
				}
				// разрешаем cross-origin доступ к ресурсу
				res.Header.Set("Access-Control-Allow-Origin", "*")

				// captchaNotRobot.check идёт через /generic_proxy на api.vk.com/api.vk.ru.
				if strings.Contains(targetAuthURL, "captchaNotRobot.check") {
					bodyBytes, readErr := io.ReadAll(res.Body)
					if readErr == nil {
						_ = res.Body.Close()
						res.Body = io.NopCloser(bytes.NewReader(bodyBytes))
						res.ContentLength = int64(len(bodyBytes))
						res.Header.Set("Content-Length", fmt.Sprint(len(bodyBytes)))
						notifyKey(keyCh, extractSuccessToken(bodyBytes))
					}
				}

				return nil
			},
		}
		genericReverse.ServeHTTP(w, r)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		Log.Debugf("[Captcha Proxy] HTTP %s %s", r.Method, r.URL.Path)
		if r.URL.Path == "/" && targetURL.Path != "" && targetURL.Path != "/" && r.URL.RawQuery == "" {
			// не логируем полный redirect URL - шум в консоли
			http.Redirect(w, r, localCaptchaURLForTarget(targetURL), http.StatusTemporaryRedirect)
			return
		}
		proxy.ServeHTTP(w, r)
	})

	return runCaptchaServerAndWait(ctx, mux, localCaptchaURLForTarget(targetURL), keyCh, "proxy HTTP server error", present)
}

func openBrowser(url string) {
	for _, cmd := range browserOpenCommands(runtime.GOOS, url) {
		// cmd.name/args приходят из жёстко закодированного browserOpenCommands;
		// внешнего ввода в exec.Command нет (url передан внутри args как обычная строка).
		if err := exec.Command(cmd.name, cmd.args...).Start(); err == nil { //nolint:noctx,gosec // hardcoded browser binary list, no taint
			return
		}
	}
}

func browserOpenCommands(goos string, url string) []browserCommand {
	switch goos {
	case "windows":
		// 'rundll32 url.dll,FileProtocolHandler' надёжнее 'cmd /c start' -
		// не задействует shell (cmd.exe), нет проблем с '&' и спец-символами.
		return []browserCommand{
			{name: "rundll32", args: []string{"url.dll,FileProtocolHandler", url}},
			// fallback с пустым title для 'start' - обход проблем с кавычками
			{name: "cmd", args: []string{"/c", "start", "", url}},
		}
	case "darwin":
		return []browserCommand{{name: "open", args: []string{url}}}
	case "linux":
		return []browserCommand{
			{name: "xdg-open", args: []string{url}},
			{name: "gio", args: []string{"open", url}},
		}
	case "android":
		return []browserCommand{
			{name: "termux-open-url", args: []string{url}},
			{name: "/system/bin/am", args: []string{"start", "-a", "android.intent.action.VIEW", "-d", url}},
			{name: "am", args: []string{"start", "-a", "android.intent.action.VIEW", "-d", url}},
			{name: "xdg-open", args: []string{url}},
		}
	case "ios":
		return []browserCommand{
			{name: "open", args: []string{url}},
			{name: "uiopen", args: []string{url}},
		}
	}
	return nil
}
