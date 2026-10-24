package localdeps

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"

	mcpsrv "github.com/hoaxisr/awg-manager/internal/mcp"
)

// sanitizeLabel makes third-party text safe to hand to a model. A
// provider chooses its server names, and a name with a newline in it is a
// second line in the model's context. Control characters and line
// separators become a space, format characters (bidi overrides,
// zero-width) are dropped, runs of space collapse, and the result is
// capped on a rune boundary.
func sanitizeLabel(s string) string { return sanitizeText(s, mcpsrv.MaxSingboxLabelRunes) }

// maxMessageRunes caps a generated message (a router issue): longer than a
// label, still one line.
const maxMessageRunes = 300

// maxSettingRunes caps a list the user typed into one setting (ports,
// subnets). It is far above any real list: cutting one short would read
// as "not listed".
const maxSettingRunes = 8192

// sanitizeText is sanitizeLabel with a cap of the caller's choosing.
func sanitizeText(s string, maxRunes int) string {
	spaced := strings.Map(func(r rune) rune {
		switch {
		case unicode.IsControl(r), r == '\u2028', r == '\u2029':
			return ' '
		case unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, s)
	clean := strings.Join(strings.Fields(spaced), " ")
	if runes := []rune(clean); len(runes) > maxRunes {
		clean = strings.TrimSpace(string(runes[:maxRunes]))
	}
	return clean
}

// hostOf returns the host of a subscription URL and nothing else: the
// token usually sits in the path, which is why redactURL (host and path)
// is not used for subscriptions. Only an http or https URL has a host
// worth naming: in happ://crypt4/… the "host" is the name of an
// encryption scheme. The host itself is the user's or the provider's
// text, so it goes through hostShaped.
func hostOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	if scheme := strings.ToLower(u.Scheme); scheme != "http" && scheme != "https" {
		return ""
	}
	return hostShaped(u.Host)
}

// probeURL returns a group's test URL as scheme, host and path. It is the
// user's setting, a public endpoint as a rule; userinfo, query and fragment
// are dropped all the same, as for any URL this package hands out, and so
// is anything that is not an http or https URL with a host-shaped host.
func probeURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	if scheme := strings.ToLower(u.Scheme); scheme != "http" && scheme != "https" {
		return ""
	}
	host := hostShaped(u.Host)
	if host == "" {
		return ""
	}
	path := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return -1
		}
		return r
	}, u.EscapedPath())
	return strings.ToLower(u.Scheme) + "://" + host + path
}

var (
	hostShape  = regexp.MustCompile(`^[A-Za-z0-9._:\[\]-]{1,253}$`)
	tokenShape = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
)

// hostShaped returns s when it looks like a host or an address, and ""
// otherwise. A subscription server's address is written by the provider;
// the daemon checks only that it is not empty. Letters, digits, dots,
// hyphens, underscores, colons and brackets cover host names, IPv4 and
// IPv6; anything else is text, and text from a provider is not passed on.
func hostShaped(s string) string {
	s = strings.TrimSpace(s)
	if !hostShape.MatchString(s) {
		return ""
	}
	return s
}

// token returns s lower-cased when it is a short identifier — letters,
// digits, hyphen, underscore, at most 32 bytes — and "" otherwise. For
// fields that hold one word from a known family (protocol, transport,
// security) and must not hold a sentence.
func token(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if !tokenShape.MatchString(s) {
		return ""
	}
	return s
}
