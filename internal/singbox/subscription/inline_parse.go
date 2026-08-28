package subscription

import (
	"strings"

	"github.com/hoaxisr/awg-manager/internal/singbox/vlink"
)

// parseInlineImportBody parses inline «группа серверов» paste that may mix
// share-links, TrustTunnel connect URLs, and AdGuard TrustTunnel client TOML
// blocks (often pasted after several links in the same textarea).
func parseInlineImportBody(body []byte) vlink.BatchResult {
	if len(body) == 0 {
		return vlink.BatchResult{}
	}
	if vlink.IsTrustTunnelClientTOML(body) {
		return vlink.ParseTrustTunnelClientTOML(body)
	}

	out := vlink.BatchResult{}
	tomlBlocks, linkBody := extractTrustTunnelTOMLBlocks(body)
	for _, block := range tomlBlocks {
		if !vlink.IsTrustTunnelClientTOML([]byte(block)) {
			continue
		}
		res := vlink.ParseTrustTunnelClientTOML([]byte(block))
		out.Outbounds = append(out.Outbounds, res.Outbounds...)
		out.Errors = append(out.Errors, res.Errors...)
	}

	linkRes := vlink.ParseBatch(NormalizeBody([]byte(linkBody), "text/plain"))
	out.Outbounds = append(out.Outbounds, linkRes.Outbounds...)
	out.Errors = append(out.Errors, linkRes.Errors...)
	out.SkippedVmess += linkRes.SkippedVmess
	out.SkippedUnsupp += linkRes.SkippedUnsupp
	return out
}

func extractTrustTunnelTOMLBlocks(body []byte) (blocks []string, linkBody string) {
	s := strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(string(body))
	lines := strings.Split(s, "\n")

	var tomlBuf []string
	var linkLines []string
	inToml := false

	flushToml := func() {
		if len(tomlBuf) == 0 {
			return
		}
		block := strings.TrimSpace(strings.Join(tomlBuf, "\n"))
		if block != "" {
			blocks = append(blocks, block)
		}
		tomlBuf = nil
		inToml = false
	}

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if inToml {
				tomlBuf = append(tomlBuf, line)
			}
			continue
		}
		if isInlineImportLinkLine(trimmed) {
			flushToml()
			linkLines = append(linkLines, line)
			continue
		}
		if !inToml && isTrustTunnelTOMLStart(trimmed) {
			inToml = true
		}
		if inToml {
			tomlBuf = append(tomlBuf, line)
			continue
		}
		// Non-link, non-TOML line — keep for link normalizer (ignored if junk).
		linkLines = append(linkLines, line)
	}
	flushToml()
	return blocks, strings.Join(linkLines, "\n")
}

func isTrustTunnelTOMLStart(line string) bool {
	lower := strings.ToLower(line)
	return strings.HasPrefix(lower, "loglevel") ||
		strings.HasPrefix(lower, "vpn_mode") ||
		strings.HasPrefix(lower, "[endpoint]") ||
		strings.HasPrefix(lower, "post_quantum") ||
		strings.HasPrefix(lower, "killswitch_")
}

func isInlineImportLinkLine(line string) bool {
	if line == "" {
		return false
	}
	lower := strings.ToLower(line)
	if vlink.IsTrustTunnelConnectURL(line) {
		return true
	}
	if strings.HasPrefix(lower, "tt://") {
		return true
	}
	if vlink.IsTrustTunnelRawPayload(line) {
		return true
	}
	return shareURLStartPlain.MatchString(line)
}
