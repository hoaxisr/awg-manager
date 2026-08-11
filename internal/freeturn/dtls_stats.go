package freeturn

import (
	"regexp"
	"strconv"
	"strings"
)

var freeturnStreamLineRE = regexp.MustCompile(`\[STREAM (\d+)\]`)

var dtlsEstablishedMarkers = []string{
	"Established DTLS connection",
}

var dtlsDownMarkers = []string{
	"Closed DTLS connection", // ft-client 2.0.x+
	"DTLS connection closed",
	"DTLS handshake failed",
	"DTLS disconnected",
	"stream closed",
	"[FATAL]",
}

// countActiveDTLSConnections returns how many freeturn streams currently have
// an established DTLS session according to the process log tail. Per-stream
// state is derived from the last matching line for each [STREAM N] id.
func countActiveDTLSConnections(log string) int {
	n, _ := dtlsTelemetry(log)
	return n
}

// dtlsTelemetry additionally reports whether the visible log tail says anything
// about stream state at all. Маркер «Established DTLS connection» одноразовый, а
// ринг держит 500 строк — у болтливого клиента он вытесняется, и пустая карта
// состояний означает «не знаем», а не «сессий нет».
func dtlsTelemetry(log string) (active int, known bool) {
	if log == "" {
		return 0, false
	}
	states := make(map[int]bool)
	for _, line := range strings.Split(log, "\n") {
		m := freeturnStreamLineRE.FindStringSubmatch(line)
		if len(m) < 2 {
			continue
		}
		id, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		switch {
		case lineMatchesAny(line, dtlsEstablishedMarkers):
			states[id] = true
		case lineMatchesAny(line, dtlsDownMarkers):
			states[id] = false
		}
	}
	if len(states) == 0 {
		return 0, false
	}
	n := 0
	for _, up := range states {
		if up {
			n++
		}
	}
	return n, true
}

func lineMatchesAny(line string, markers []string) bool {
	for _, m := range markers {
		if strings.Contains(line, m) {
			return true
		}
	}
	return false
}
