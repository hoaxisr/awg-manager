package server

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/tgwebproxy/tproxy/config"
	"github.com/hoaxisr/awg-manager/internal/tgwebproxy/tproxy/session"
)

func (s *Server) bridgeProfile(r *http.Request) *config.Profile {
	if r.Method != http.MethodGet || r.URL.EscapedPath() != s.base ||
		len(r.URL.RawQuery) != len("bridge=")+43 ||
		!strings.HasPrefix(r.URL.RawQuery, "bridge=") {
		return nil
	}
	text := strings.TrimPrefix(r.URL.RawQuery, "bridge=")
	value, err := base64.RawURLEncoding.Strict().DecodeString(text)
	if err != nil {
		return nil
	}
	return s.manager.MatchCapability(value)
}

// Inspect metadata before interpreting credential syntax. Parsing only the
// canonical Authorization or bridge fields would leak real secrets in duplicate
// headers, malformed queries, cookies, referrers, or wrong-path requests. Bodies
// and trailers are deliberately not read: public uploads must remain streaming.
func (s *Server) hasInternalSecret(r *http.Request) bool {
	scan := secretScan{manager: s.manager}
	if scan.carrierCredential(r, s.config.LegacyTokenDrain) {
		return true
	}
	if scan.contains(r.URL.String()) || scan.contains(r.Host) {
		return true
	}
	for name, values := range r.Header {
		if scan.contains(name) {
			return true
		}
		for _, value := range values {
			if scan.contains(value) {
				return true
			}
		}
	}
	return false
}

// carrierCredential looks for a canonical token in the fields where the bridge
// page sends one: any such token while draining, otherwise a signed one, with
// or without the filter tag.
func (scan *secretScan) carrierCredential(r *http.Request, drain bool) bool {
	for _, value := range r.Header.Values("Authorization") {
		if token, ok := bearerToken(value); ok && (drain || scan.signed(token)) {
			return true
		}
	}
	// Canonical spelling: Values would allocate to canonicalize it per request.
	for _, value := range r.Header["Sec-Websocket-Protocol"] {
		for _, protocol := range strings.Split(value, ",") {
			token, _, _, ok := webSocketCredentials(strings.TrimSpace(protocol))
			if ok {
				if _, ok := bearerToken("Bearer " + token); ok && (drain || scan.signed(token)) {
					return true
				}
			}
		}
	}
	return false
}

// secretScan checks every 43-character window of every base64 run. Clients
// choose that metadata before any limit applies, and only an authentic secret
// may change the response, so a window must not allocate or rekey the MAC.
type secretScan struct {
	manager *session.Manager
	tokens  *session.TokenClassifier
	decoded []byte
}

func (scan *secretScan) classifier() *session.TokenClassifier {
	if scan.tokens == nil {
		scan.tokens = scan.manager.NewTokenClassifier()
	}
	return scan.tokens
}

func (scan *secretScan) signed(token string) bool {
	var decoded [32]byte
	n, err := base64.RawURLEncoding.Decode(decoded[:], []byte(token))
	return err == nil && n == len(decoded) && scan.classifier().Signed(decoded[:])
}

func (scan *secretScan) contains(text string) bool {
	// Decode individual escapes so a malformed escape elsewhere cannot hide a
	// capability. Do not parse/re-encode the public query: even malformed query
	// strings belong to the application when they carry no authentic secret.
	if strings.Contains(text, "%") {
		var decoded strings.Builder
		for i := 0; i < len(text); i++ {
			if text[i] == '%' && i+2 < len(text) {
				if value, err := url.PathUnescape(text[i : i+3]); err == nil {
					decoded.WriteString(value)
					i += 2
					continue
				}
			}
			decoded.WriteByte(text[i])
		}
		text = decoded.String()
	}
	start := 0
	for i := 0; i <= len(text); i++ {
		if i < len(text) && base64Byte(text[i]) {
			continue
		}
		if scan.containsInRun(text[start:i]) {
			return true
		}
		start = i + 1
	}
	return false
}

// A window starting at offset phase+4*j decodes to the same 32 bytes as the
// run decoded from phase, at 3*j: whole quanta map to whole byte triples, and
// the window's 43rd character contributes only the two bits lenient decoding
// keeps. So four decodes of the run cover every offset.
func (scan *secretScan) containsInRun(run string) bool {
	const windowChars, windowBytes = 43, 32
	if len(run) < windowChars {
		return false
	}
	tokens := scan.classifier()
	for phase := 0; phase < 4 && phase+windowChars <= len(run); phase++ {
		chars := run[phase:]
		if len(chars)%4 == 1 {
			chars = chars[:len(chars)-1]
		}
		if need := base64.RawURLEncoding.DecodedLen(len(chars)); cap(scan.decoded) < need {
			scan.decoded = make([]byte, need)
		}
		decoded := scan.decoded[:cap(scan.decoded)]
		n, err := base64.RawURLEncoding.Decode(decoded, []byte(chars))
		if err != nil {
			continue
		}
		for offset := 0; offset+windowBytes <= n; offset += 3 {
			value := decoded[offset : offset+windowBytes]
			if tokens.Classify(value) != session.TokenExternal ||
				scan.manager.MatchCapability(value) != nil {
				return true
			}
		}
	}
	return false
}

func base64Byte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' || value == '-' || value == '_'
}
