package freeturn

import (
	"strconv"
	"strings"
)

// CaptchaLogSummary is derived from freeturn-client stderr for UI hints.
type CaptchaLogSummary struct {
	// PendingStreams: VK-auth streams that still need manual captcha or retry.
	PendingStreams int `json:"pendingStreams,omitempty"`
	// PortContention: another stream already holds :8765 (bind: address already in use).
	PortContention bool `json:"portContention,omitempty"`
	// CaptchaSession: how many times manual captcha was triggered this run (iframe reload key).
	CaptchaSession int `json:"captchaSession,omitempty"`
}

var captchaPortBusyMarkers = []string{
	"address already in use",
}

var captchaStreamPendingMarkers = []string{
	"Triggering manual captcha fallback",
	"CAPTCHA_WAIT_REQUIRED",
	"manual captcha failed",
	"Falling back to manual captcha",
}

var captchaStreamResolvedMarkers = []string{
	"Got token from browser",
	"Established DTLS connection",
}

// analyzeCaptchaLog inspects process output for multi-stream manual captcha state.
func analyzeCaptchaLog(log string) CaptchaLogSummary {
	if log == "" {
		return CaptchaLogSummary{}
	}
	pending := make(map[int]struct{})
	out := CaptchaLogSummary{}

	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, "Triggering manual captcha fallback") {
			out.CaptchaSession++
		}
		if lineMatchesAny(line, captchaPortBusyMarkers) && strings.Contains(line, "8765") {
			out.PortContention = true
		}

		streamID := parseFreeturnStreamID(line)
		if streamID <= 0 {
			continue
		}

		switch {
		case lineMatchesAny(line, captchaStreamResolvedMarkers):
			delete(pending, streamID)
		case lineMatchesAny(line, captchaStreamPendingMarkers):
			pending[streamID] = struct{}{}
		}
	}

	out.PendingStreams = len(pending)
	return out
}

func parseFreeturnStreamID(line string) int {
	m := freeturnStreamLineRE.FindStringSubmatch(line)
	if len(m) < 2 {
		return 0
	}
	id, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return id
}
