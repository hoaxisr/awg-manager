package signature

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// MaxTagBytes is the per-tag ceiling for a single <r>/<rc>/<rd> token.
//
// Provenance, because the number is widely miscited as a current AmneziaWG
// rule: it was enforced only by amneziawg-go, in newRandomGeneratorBase
// (device/awg/tag_generator.go:73 as of v0.2.15) — "size must be less than
// 1000" — and was deleted on 2025-12-01 by 0361c54 ("fix: refactor processing
// of junk packets", PR #103). Neither amneziawg-tools nor the AmneziaWG Linux
// kernel module ever had it, and awg_proxy.ko allows up to 100000
// (kmod/awg-proxy/src/cps.c).
//
// So no current AmneziaWG implementation rejects an oversized token, and a
// config carrying one is not malformed by today's upstream. Keenetic's NDMS
// ASC nonetheless refuses such a value, which is consistent with its parser
// having been derived from pre-PR-103 amneziawg-go. Measured on
// 5.01.C.3.0-1 via RCI `wireguard asc`: <r 1000>, <rc 1000> accepted;
// <r 1001>, <rc 1001>, <rd 1001> rejected with `invalid I1 value`. We keep our
// own generated chains under the limit (splitPad) and split third-party ones
// at the NDMS boundary purely for that compatibility.
const MaxTagBytes = 1000

// MaxSplittableTagBytes bounds what we are willing to rewrite. A size beyond
// it is not a config we can rescue: awg_proxy.ko's parse_int
// (kmod/awg-proxy/src/cps.c) rejects a value once the digits read so far
// exceed 100000 (so its true ceiling is 1000009), and such a token is
// nonsense wherever it ends up.
//
// The bound is load-bearing, not cosmetic. Nothing validates I1-I5 on the way
// in — config.Parse stores the string verbatim and ValidateAWG3 checks only
// HeaderProtectionKey and S1-S4 — so an imported .conf can carry
// "<r 999999999999999999>". Expanding that would spin ~10^15 iterations
// appending to a Builder and take the process out on memory. Oversized beyond
// rescue is left exactly as it came in, to be rejected downstream as it
// should be.
const MaxSplittableTagBytes = 100000

// randTagRe matches one random-padding token. The size is required: <r> with
// no digits is rejected by the AmneziaWG parser anyway, so leaving it alone is
// the correct behaviour. Whitespace is tolerated because third-party
// generators are under no obligation to match our formatting.
var randTagRe = regexp.MustCompile(`<\s*(rc|rd|r)\s*(\d+)\s*>`)

// SplitOversizedTags rewrites any <r>/<rc>/<rd> token larger than MaxTagBytes
// into a run of tokens of the same kind that sum to the original size. Every
// matched token is re-emitted in canonical "<kind N>" form: the regex tolerates
// "<r500>" and "<r  500 >", which NDMS rejects just like an oversized token
// (measured on 5.01.C.3.0-1), and fixing only the oversized tokens would leave
// a differently malformed config behind. Everything else — <b>, <t>, <c>,
// unknown tokens, stray text — comes back byte-identical.
//
// The second value lists every token that changed, e.g.
// "<r 1178> → <r 1000><r 178>", and is "" when the spec was returned as is.
// Callers log it rather than rewrite silently: a config that only imports
// because we edited it should say so.
//
// The rewrite is wire-equivalent: N random bytes emitted as one token or as
// several are the same N bytes on the wire, so a peer sees no difference and
// already-distributed configs stay valid.
//
// This exists because generators in the wild legitimately emit tokens over the
// limit — it is no longer an upstream rule (see MaxTagBytes). The
// docker-amneziawg container's default I1 is a QUIC Initial padded to the RFC
// 9000 §14.1 minimum of 1200 bytes, whose payload lands in a single <r 1178>;
// a QUIC Initial cannot be expressed within 1000-byte tokens at all without
// splitting. Every current datapath accepts it, so it works everywhere until
// the value reaches NDMS, which refuses the whole interface with
// `"WireguardN": invalid I1 value.` — naming the slot but not the token.
func SplitOversizedTags(spec string) (out, note string) {
	if !strings.Contains(spec, "<") {
		return spec, ""
	}
	var changes []string
	out = randTagRe.ReplaceAllStringFunc(spec, func(tok string) string {
		m := randTagRe.FindStringSubmatch(tok)
		n, err := strconv.Atoi(m[2])
		if err != nil || n > MaxSplittableTagBytes {
			return tok
		}
		rep := fmt.Sprintf("<%s %d>", m[1], n)
		if n > MaxTagBytes {
			rep = splitPad(n, m[1])
		}
		if rep != tok {
			changes = append(changes, tok+" → "+rep)
		}
		return rep
	})
	return out, strings.Join(changes, ", ")
}

// RewriteLogMessage renders the app-log line for a note returned by
// SplitOversizedTags (prefixed by the caller with the slot names).
func RewriteLogMessage(note string) string {
	return fmt.Sprintf("сигнатуры переписаны под парсер NDMS (не более %d байт на тег) — %s",
		MaxTagBytes, note)
}

// splitPad renders n padding bytes as one or more <tag N> tokens, each within
// MaxTagBytes. Despite the widespread description of this as a kernel limit, it was
// only ever an amneziawg-go check, removed upstream in PR #103 — see
// MaxTagBytes above. Kept because stricter third-party parsers (notably
// Keenetic NDMS ASC) still enforce it.
func splitPad(n int, tag string) string {
	if n <= 0 {
		return ""
	}
	var sb strings.Builder
	for n > MaxTagBytes {
		fmt.Fprintf(&sb, "<%s %d>", tag, MaxTagBytes)
		n -= MaxTagBytes
	}
	fmt.Fprintf(&sb, "<%s %d>", tag, n)
	return sb.String()
}
