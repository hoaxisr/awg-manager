package freeturn

import (
	"strings"
	"time"
)

const (
	handshakeFailureMarker   = "Handshake failed"
	handshakeFailWindowLines = 40
	handshakeFailMinCount    = 6
	// logStaleMaxAge — процесс жив, DTLS «есть» по старым строкам, но новых записей нет.
	logStaleMaxAge = 45 * time.Minute
	// logStaleMinUptime — не считать лог «застывшим» сразу после старта.
	logStaleMinUptime = 2 * time.Hour
)

// logRecentHandshakeFailures reports repeated handshake timeouts in the log tail
// (freeturn-server waiting on obf/backend, or client-side reconnect attempts).
func logRecentHandshakeFailures(log string, windowLines, minCount int) bool {
	if log == "" || minCount <= 0 {
		return false
	}
	lines := strings.Split(log, "\n")
	if windowLines > 0 && len(lines) > windowLines {
		lines = lines[len(lines)-windowLines:]
	}
	n := 0
	for _, line := range lines {
		if strings.Contains(line, handshakeFailureMarker) {
			n++
		}
	}
	return n >= minCount
}

// logTailStale reports that the newest timestamped line is older than maxAge.
func logTailStale(log string, now time.Time, maxAge time.Duration) bool {
	last, ok := logLastEntryTime(log)
	if !ok {
		return false
	}
	return now.Sub(last) > maxAge
}

func logLastEntryTime(log string) (time.Time, bool) {
	lines := strings.Split(log, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t, ok := parseFreeturnLogTime(lines[i]); ok {
			return t, true
		}
	}
	return time.Time{}, false
}

// clientPeerStaleZombie: DTLS-сессии «есть» по маркерам в ринге, но процесс
// давно ничего не пишет — типично после рестарта peer-сервера или обрыва WAN.
func clientPeerStaleZombie(st ProcessStatus, now time.Time) bool {
	active, known := dtlsTelemetry(st.Log)
	if !known || active == 0 {
		return false
	}
	if now.Sub(*st.StartedAt) < logStaleMinUptime {
		return false
	}
	return logTailStale(st.Log, now, logStaleMaxAge)
}
