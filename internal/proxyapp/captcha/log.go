package captcha

import (
	"regexp"
	"strconv"
	"strings"
)

// LogSummary is derived from freeturn-client stderr for UI hints.
type LogSummary struct {
	// PendingStreams: VK-auth streams that still need manual captcha or retry.
	PendingStreams int `json:"pendingStreams,omitempty"`
	// PortContention: another stream already holds :8765 (bind: address already in use).
	PortContention bool `json:"portContention,omitempty"`
	// CaptchaSession: how many times manual captcha was triggered this run (iframe reload key).
	CaptchaSession int `json:"captchaSession,omitempty"`
}

// streamLineRE и lineMatchesAny перенесены из freeturn/dtls_stats.go: разбор
// журнала капчи стоял на них, а счётчик DTLS-соединений в новом мире приходит
// снимком (awgmproto.State.Clients), поэтому весь dtls_stats.go не переносится.
var streamLineRE = regexp.MustCompile(`\[STREAM (\d+)\]`)

var portBusyMarkers = []string{
	"address already in use",
}

var streamPendingMarkers = []string{
	"Triggering manual captcha fallback",
	"CAPTCHA_WAIT_REQUIRED",
	"manual captcha failed",
	"Falling back to manual captcha",
}

var streamResolvedMarkers = []string{
	"Got token from browser",
	"Established DTLS connection",
}

// analyzeLog inspects process output for multi-stream manual captcha state.
func analyzeLog(log string) LogSummary {
	if log == "" {
		return LogSummary{}
	}
	pending := make(map[int]struct{})
	out := LogSummary{}

	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, "Triggering manual captcha fallback") {
			out.CaptchaSession++
		}
		if lineMatchesAny(line, portBusyMarkers) && strings.Contains(line, "8765") {
			out.PortContention = true
		}

		streamID := parseStreamID(line)
		if streamID <= 0 {
			continue
		}

		switch {
		case lineMatchesAny(line, streamResolvedMarkers):
			delete(pending, streamID)
		case lineMatchesAny(line, streamPendingMarkers):
			pending[streamID] = struct{}{}
		}
	}

	out.PendingStreams = len(pending)
	return out
}

func parseStreamID(line string) int {
	m := streamLineRE.FindStringSubmatch(line)
	if len(m) < 2 {
		return 0
	}
	id, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return id
}

func lineMatchesAny(line string, markers []string) bool {
	for _, m := range markers {
		if strings.Contains(line, m) {
			return true
		}
	}
	return false
}
