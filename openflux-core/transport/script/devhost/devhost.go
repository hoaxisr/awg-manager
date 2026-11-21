// Package devhost plays the app's part for a script transport that is being
// developed: it shows the pages the script raises (needsSetup /
// captchaRequired) in the developer's own browser, gives them
// window.openfluxSubmit, and hands what a page submits back to the script
// the way the apps do (FlattenSubmission, then ApplyCookies, which fires
// onEvent("cookiesApplied")). A script author can therefore walk the whole
// setup flow with scripttest, before any app is involved.
//
// It mirrors the apps' contract, and is the reference for it:
//
//   - an inline html page is served as it is, with the bridge put in before
//     the page's own scripts;
//   - an http://127.0.0.1:port page (the script's own httpserver.listen()) is
//     reached through this host, which puts the same bridge into its HTML;
//   - an https page is a real site: it is opened in the browser, but a CLI
//     cannot collect its cookies - hand them over with scripttest
//     -cookies-file.
package devhost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/p1neappleXpress/OpenFlux/transport/script"
)

// submitPath is where the bridge posts; it is reserved on the dev server.
const submitPath = "/__openflux/submit"

// maxPage bounds an HTML response the proxy rewrites.
const maxPage = 4 << 20

// Bridge is the script put into a page: window.openfluxSubmit(payload) posts
// the payload to the host and returns a Promise of its answer ({ok, error}).
// The apps' bridge returns nothing; do not rely on the return value.
// "openflux-ready" fires on window once it is defined.
const Bridge = `<script>(function(){if(window.openfluxSubmit)return;` +
	`window.openfluxSubmit=function(p){return fetch("` + submitPath + `",{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify(p)}).then(function(r){return r.json()})};` +
	`try{window.dispatchEvent(new Event("openflux-ready"))}catch(e){}})();</script>`

// Options configure a Host. Zero values are usable.
type Options struct {
	// Logf receives what happens (page shown, submission delivered). It never
	// gets submitted values, only their names.
	Logf func(format string, args ...interface{})
	// Open shows a URL to the developer, e.g. in their browser. nil: only log it.
	Open func(url string) error
	// AutoSubmit, if set, is delivered as soon as a page is raised, as if the
	// user had filled and submitted it: for running a script in CI.
	AutoSubmit []byte
}

// Host serves one script transport's setup pages. Create it with New, give
// tr.SetErrorNotifier(h.Notify), and Close it when done.
type Host struct {
	tr   *script.ScriptTransport
	opts Options

	// sink, when set (ShowPage), takes each submission instead of the transport.
	sink func(values map[string]string) error

	mu      sync.Mutex
	ln      net.Listener
	srv     *http.Server
	page    string   // inline html currently shown
	target  *url.URL // the script's own server currently shown
	proxy   *httputil.ReverseProxy
	pageURL string
}

// New returns a Host for tr.
func New(tr *script.ScriptTransport, opts Options) *Host {
	if opts.Logf == nil {
		opts.Logf = func(string, ...interface{}) {}
	}
	return &Host{tr: tr, opts: opts}
}

// ShowPage serves a page of its own (the settings wizard of a script that is
// not running) and hands what it submits to sink instead of a transport; the
// page's openfluxSubmit gets sink's error back as {ok:false, error}. The Host
// may be made with a nil transport for this.
func (h *Host) ShowPage(page string, sink func(values map[string]string) error) {
	h.mu.Lock()
	h.sink = sink
	h.mu.Unlock()
	h.show(page, nil)
}

// Notify is the transport.ErrorNotifier callback: wire it with
// tr.SetErrorNotifier(h.Notify).
func (h *Host) Notify(_ error, name, addr, page, reason string) {
	h.opts.Logf("%s asks for setup (%s)", name, orDash(reason))
	switch {
	case page != "":
		h.show(page, nil)
	case script.IsOwnPage(addr, ""):
		target, err := url.Parse(addr)
		if err != nil {
			h.opts.Logf("setup page address %q is not usable: %v", addr, err)
			return
		}
		h.show("", target)
	default:
		h.opts.Logf("a real site: open %s, sign in, and pass the cookies with -cookies-file", addr)
		if h.opts.Open != nil {
			_ = h.opts.Open(addr)
		}
		return
	}
	if len(h.opts.AutoSubmit) > 0 {
		go func() {
			time.Sleep(200 * time.Millisecond)
			if err := h.deliver(h.opts.AutoSubmit); err != nil {
				h.opts.Logf("auto-submit failed: %v", err)
			}
		}()
	}
}

func (h *Host) show(page string, target *url.URL) {
	h.mu.Lock()
	h.page, h.target = page, target
	if target != nil {
		h.proxy = h.newProxy(target)
	} else {
		h.proxy = nil
	}
	err := h.listenLocked()
	pageURL := h.pageURL
	h.mu.Unlock()
	if err != nil {
		h.opts.Logf("cannot serve the setup page: %v", err)
		return
	}
	h.opts.Logf("setup page: %s", pageURL)
	if h.opts.Open != nil {
		_ = h.opts.Open(pageURL)
	}
}

func (h *Host) listenLocked() error {
	if h.srv != nil {
		return nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	h.ln = ln
	h.pageURL = "http://" + ln.Addr().String() + "/"
	srv := &http.Server{Handler: http.HandlerFunc(h.serve), ReadHeaderTimeout: 10 * time.Second}
	h.srv = srv
	go func() { _ = srv.Serve(ln) }()
	return nil
}

// PageURL is where the current page is served ("" before any was raised).
func (h *Host) PageURL() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pageURL
}

// Close stops the dev server.
func (h *Host) Close() {
	h.mu.Lock()
	srv := h.srv
	h.srv = nil
	h.mu.Unlock()
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
}

func (h *Host) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == submitPath {
		h.serveSubmit(w, r)
		return
	}
	h.mu.Lock()
	page, proxy := h.page, h.proxy
	h.mu.Unlock()
	switch {
	case proxy != nil:
		proxy.ServeHTTP(w, r)
	case page != "" && (r.URL.Path == "/" || r.URL.Path == "/index.html"):
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, InjectBridge(page))
	default:
		http.NotFound(w, r)
	}
}

func (h *Host) serveSubmit(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = io.WriteString(w, `{"ok":false,"error":"POST only"}`)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err == nil {
		err = h.deliver(body)
	}
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		out, _ := json.Marshal(map[string]interface{}{"ok": false, "error": err.Error()})
		_, _ = w.Write(out)
		return
	}
	_, _ = io.WriteString(w, `{"ok":true}`)
}

// deliver is what an app does with a submission.
func (h *Host) deliver(body []byte) error {
	values, err := script.FlattenSubmission(body)
	if err != nil {
		return err
	}
	h.mu.Lock()
	sink := h.sink
	h.mu.Unlock()
	switch {
	case sink != nil:
		if err := sink(values); err != nil {
			return err
		}
	case h.tr != nil:
		if err := h.tr.ApplyCookies(values); err != nil {
			return err
		}
	default:
		return fmt.Errorf("no script is running to take the data")
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h.opts.Logf("setup delivered to the script: %d values (%s)", len(values), strings.Join(keys, ", "))
	return nil
}

func (h *Host) newProxy(target *url.URL) *httputil.ReverseProxy {
	rp := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: target.Host})
	director := rp.Director
	rp.Director = func(r *http.Request) {
		director(r)
		r.Header.Del("Accept-Encoding") // the HTML must come back readable
	}
	rp.ModifyResponse = func(resp *http.Response) error {
		if !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/html") {
			return nil
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxPage))
		_ = resp.Body.Close()
		if err != nil {
			return err
		}
		out := []byte(InjectBridge(string(body)))
		resp.Body = io.NopCloser(bytes.NewReader(out))
		resp.ContentLength = int64(len(out))
		resp.Header.Set("Content-Length", strconv.Itoa(len(out)))
		return nil
	}
	return rp
}

// InjectBridge puts Bridge into an HTML document where it runs before the
// page's own scripts and keeps standards mode: after <head>, else after
// <html>, else after the doctype, else in front. A script in front of the
// doctype would throw the page into quirks mode.
func InjectBridge(page string) string {
	return InjectSnippet(page, Bridge)
}

// InjectSnippet is InjectBridge for any snippet (the apps use their own
// bridge, written the same way).
func InjectSnippet(page, snippet string) string {
	lower := asciiLower(page)
	for _, tag := range []string{"<head", "<html"} {
		if i := strings.Index(lower, tag); i >= 0 {
			// The tag must really be the tag (<header is not <head).
			end := i + len(tag)
			if end < len(lower) && (lower[end] == '>' || lower[end] == ' ' || lower[end] == '\t' || lower[end] == '\n' || lower[end] == '\r' || lower[end] == '/') {
				if j := strings.IndexByte(lower[end:], '>'); j >= 0 {
					at := end + j + 1
					return page[:at] + snippet + page[at:]
				}
			}
		}
	}
	if strings.HasPrefix(strings.TrimSpace(lower), "<!doctype") {
		if j := strings.IndexByte(lower, '>'); j >= 0 {
			return page[:j+1] + snippet + page[j+1:]
		}
	}
	return snippet + page
}

// asciiLower lowercases A-Z only, so every index into the result is an index
// into s (strings.ToLower can change a string's length, e.g. İ).
func asciiLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// String names the host in logs.
func (h *Host) String() string { return fmt.Sprintf("devhost(%s)", h.PageURL()) }
