package logging

import (
	"net/url"
	"regexp"
	"strings"
)

var (
	// A scheme, "://", and a tail up to whitespace, a double quote, angle
	// brackets or a backtick — how net/http and url.Parse quote a URL.
	// Userinfo may start with anything a URL allows (a percent-encoded
	// password, base64 starting with + or /), so the tail's first
	// character is only kept from being a comma or a closing bracket: the
	// daemon's own hints list bare schemes ("vless://, trojan://"), and
	// those are not addresses. No word boundary before the scheme: a
	// scheme glued to a word ("x_https://…") is still a URL.
	urlInTextRe = regexp.MustCompile("(?i)[a-z][a-z0-9+.-]{0,15}://[^\\s\"<>`,)\\]][^\\s\"<>`]*")
	// A host worth keeping has a dot (or is a bracketed IPv6 literal).
	// The legacy ss:// link is one base64 blob that url.Parse reads as
	// the host; base64 has no dot, so it never passes.
	keptHostRe = regexp.MustCompile(`^(?:(?:[A-Za-z0-9-]+\.)+[A-Za-z0-9-]+|\[[0-9A-Fa-f:.]+\])(?::[0-9]{1,5})?$`)
)

// trailingPunct is sentence punctuation that follows a URL in prose and
// is not part of it; it is put back after the reduced URL.
const trailingPunct = ".,;:!?)]'"

// RedactURLs reduces every URL in s to its scheme and host. A
// subscription's address carries its token in the path, the query or the
// userinfo, and a share link carries the server's uuid in the userinfo;
// net/http and url.Parse quote such an address whole in their errors —
// the address of a redirect hop too, which no exact-match replacement of
// the configured URL can catch. The host is what a reader needs to tell
// which download failed, and SanitizeLogText masks it further on display.
//
// Service.AppLog applies it to every journal line; a service applies it
// itself only to text it stores (a subscription's lastError), which the
// journal does not see. One case it cannot catch: a password written
// with a literal space ends the match at the space.
func RedactURLs(s string) string {
	if !strings.Contains(s, "://") {
		return s
	}
	return urlInTextRe.ReplaceAllStringFunc(s, func(m string) string {
		core := strings.TrimRight(m, trailingPunct)
		tail := m[len(core):]
		scheme, _, _ := strings.Cut(core, "://")
		u, err := url.Parse(core)
		if err != nil || !keptHostRe.MatchString(u.Host) {
			return scheme + "://<redacted>" + tail
		}
		if u.User == nil && (u.Path == "" || u.Path == "/") && u.RawQuery == "" && u.Fragment == "" && u.Opaque == "" {
			return scheme + "://" + u.Host + u.Path + tail
		}
		return scheme + "://" + u.Host + "/…" + tail
	})
}
