package wdtt

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

var wdttStatsLineRE = regexp.MustCompile(`\[СТАТИСТИКА\]\s*Активных:\s*(\d+)`)

const (
	// stallMinEvents — минимум STATS-сэмплов в окне (~30 с при интервале 3 с).
	stallMinEvents = 10
	// stallMinUpBytes — keepalive уходит, но ответа от peer нет.
	stallMinUpBytes = int64(400)
)

// StatsSnapshot — последний срез __WDTT_EVENT__|STATS| из лога wdtt-client.
type StatsSnapshot struct {
	Active    int
	BytesDown int64
	BytesUp   int64
}

// ExtractActiveConnectionsFromLog returns the latest active worker count from
// wdtt-client stdout: __WDTT_EVENT__|STATS|{"active":N,...} or the Russian
// [СТАТИСТИКА] line emitted every 3 seconds.
func ExtractActiveConnectionsFromLog(log string) int {
	n, _ := activeTelemetry(log)
	return n
}

// activeTelemetry additionally reports whether the log tail carries stats at
// all: отсутствие строк — это «не знаем», а не «активных ноль» (клиент может
// быть жив, но молчалив либо печатать статистику в другом формате).
func activeTelemetry(log string) (active int, known bool) {
	events := ExtractStatsSnapshotsFromLog(log)
	if len(events) > 0 {
		return events[len(events)-1].Active, true
	}
	if n := extractActiveFromStatsLines(log); n >= 0 {
		return n, true
	}
	return 0, false
}

// ExtractStatsSnapshotsFromLog parses all STATS events from wdtt-client stdout.
func ExtractStatsSnapshotsFromLog(log string) []StatsSnapshot {
	const prefix = "__WDTT_EVENT__|STATS|"
	var out []StatsSnapshot
	for _, line := range strings.Split(log, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		payload := strings.TrimPrefix(line, prefix)
		var data struct {
			Active    int   `json:"active"`
			BytesDown int64 `json:"bytes_down"`
			BytesUp   int64 `json:"bytes_up"`
		}
		if err := json.Unmarshal([]byte(payload), &data); err != nil {
			continue
		}
		out = append(out, StatsSnapshot{
			Active:    data.Active,
			BytesDown: data.BytesDown,
			BytesUp:   data.BytesUp,
		})
	}
	return out
}

// ClientTrafficStalled reports a zombie relay: workers look active but only
// uplink keepalives grow while downstream bytes stay flat (server restart /
// relay stale on the remote side).
func ClientTrafficStalled(log string) bool {
	events := ExtractStatsSnapshotsFromLog(log)
	if len(events) < stallMinEvents {
		return false
	}
	recent := events[len(events)-stallMinEvents:]
	first := recent[0]
	last := recent[len(recent)-1]
	if last.Active == 0 {
		return false
	}
	for _, e := range recent {
		if e.Active == 0 || e.BytesDown != last.BytesDown {
			return false
		}
	}
	return last.BytesUp >= first.BytesUp+stallMinUpBytes
}

func extractActiveFromStatsLines(log string) int {
	last := -1
	for _, line := range strings.Split(log, "\n") {
		m := wdttStatsLineRE.FindStringSubmatch(line)
		if len(m) != 2 {
			continue
		}
		if n, err := strconv.Atoi(m[1]); err == nil {
			last = n
		}
	}
	return last
}
