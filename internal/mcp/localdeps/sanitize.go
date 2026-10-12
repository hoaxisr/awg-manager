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
func sanitizeLabel(s string) string {
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
	if runes := []rune(clean); len(runes) > mcpsrv.MaxSingboxLabelRunes {
		clean = strings.TrimSpace(string(runes[:mcpsrv.MaxSingboxLabelRunes]))
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
