package script

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

// What a script may ask the app to show, and what the core lets through.
//
// raise("needsSetup" | "captchaRequired", {html | url, reason}) reaches the
// app, which opens the page in its built-in browser. The page is one of
// three things:
//
//   - a real site (an https url): the user signs in or passes a check, the
//     app collects the site's cookies;
//   - the script's own inline page (html): drawn by the app, data goes back
//     through window.openfluxSubmit;
//   - the script's own server (an http url on 127.0.0.1, the address of an
//     httpserver.listen() the same script started): same hand-back.
//
// The last two are "own" pages (IsOwnPage): the app does not look for
// cookies in them, and offers them window.openfluxSubmit. Everything else a
// script could put in url (file:, javascript:, data:, a plain http site, a
// loopback port that is not its own) would open something the user never
// asked for, so raise() refuses it with a TypeError the script sees while it
// is being written, not a silent drop in production.

const (
	// maxSetupHTML keeps a page inside one IPC frame (ipc.MaxFrameBytes is
	// 1 MiB, the page is wrapped in JSON) with room to spare.
	maxSetupHTML = 512 << 10
	// maxSetupReason is the longest reason that is kept; the apps show it
	// as the title of the setup dialog.
	maxSetupReason = 200
)

// checkSetupURL decides whether raw may be handed to the app as a page to
// open. https is any site; http only the loopback address of a server this
// transport started itself.
func (t *ScriptTransport) checkSetupURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return fmt.Errorf("url %q is not an address", clip(raw, 80))
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && scheme != "http" {
		return fmt.Errorf("a setup page is https or the script's own http://127.0.0.1 server, not %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("url %q has no host", clip(raw, 80))
	}
	if scheme == "https" {
		return nil
	}
	host := u.Hostname()
	if !isLoopbackHost(host) {
		return fmt.Errorf("http is only for the script's own server on 127.0.0.1, not %q (use https for a site)", host)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port <= 0 {
		return fmt.Errorf("a loopback setup address needs the port of httpserver.listen(), got %q", clip(raw, 80))
	}
	if !t.ownsHTTPPort(port) {
		return fmt.Errorf("port %d is not an httpserver.listen() of this script (call listen() first, and raise with its addr)", port)
	}
	return nil
}

// checkSetupPayload validates the raise() payload of a captcha/setup kind
// and returns the reason clipped to what the apps show.
func (t *ScriptTransport) checkSetupPayload(page, addr, reason string) (string, error) {
	switch {
	case page != "" && addr != "":
		return "", fmt.Errorf("pass html (an inline page) or url (an address), not both")
	case page != "":
		if len(page) > maxSetupHTML {
			return "", fmt.Errorf("html is %d bytes, the limit is %d: serve a bigger page with httpserver.listen()", len(page), maxSetupHTML)
		}
	case addr != "":
		if err := t.checkSetupURL(addr); err != nil {
			return "", err
		}
	}
	return clip(reason, maxSetupReason), nil
}

// IsOwnPage reports whether a page a script raised is the script's own (an
// inline html page, or an http address on loopback, where only its own
// httpserver.listen() can answer: checkSetupURL let nothing else through)
// rather than a real site. The apps show an own page as it is, give it
// window.openfluxSubmit and do not collect cookies from it.
func IsOwnPage(addr, page string) bool {
	if page != "" {
		return true
	}
	u, err := url.Parse(addr)
	return err == nil && strings.EqualFold(u.Scheme, "http") && isLoopbackHost(u.Hostname())
}

// isLoopbackHost: localhost, 127.0.0.0/8 or ::1.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// AcceptRemoteCheck reports whether a check a remote exit asks the client to
// show may be shown. An exit's inline page is self-contained and fine. An
// address must be a site (http or https) that is not on loopback: it is
// opened through the exit's proxy, and a loopback address from an exit is
// either meaningless here or, if the app opened it directly, would reach
// whatever listens on this machine on that port.
func AcceptRemoteCheck(addr, page string) bool {
	if page != "" {
		return true
	}
	u, err := url.Parse(addr)
	if err != nil {
		return false
	}
	if s := strings.ToLower(u.Scheme); s != "https" && s != "http" {
		return false
	}
	host := u.Hostname()
	if host == "" || isLoopbackHost(host) {
		return false
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		return false
	}
	return true
}

// clip shortens s to n runes for an error message or a title.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

// FlattenSubmission turns what a setup page handed to window.openfluxSubmit
// into the {name: value} map that reaches the script (ApplyCookies, then
// cookieJar.get() in onEvent("cookiesApplied")).
//
// The page may pass a flat object, or {client: {...}, node: {...}} (only the
// client half is delivered today). Values arrive as strings: a string is kept,
// a number or true/false becomes its text, null, objects and arrays are
// dropped (a page that needs structure sends JSON.stringify of it). An empty
// result is an error: a page that submits nothing has not set anything up.
func FlattenSubmission(raw []byte) (map[string]string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var root map[string]interface{}
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("the page's data is not a JSON object: %w", err)
	}
	if client, ok := root["client"].(map[string]interface{}); ok {
		root = client
	}
	out := make(map[string]string, len(root))
	for k, v := range root {
		if k == "" {
			continue
		}
		switch x := v.(type) {
		case string:
			out[k] = x
		case json.Number:
			out[k] = x.String()
		case bool:
			out[k] = strconv.FormatBool(x)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("the page passed no data")
	}
	return out, nil
}
