package wdtt

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

var wdttStatsLineRE = regexp.MustCompile(`\[СТАТИСТИКА\]\s*Активных:\s*(\d+)`)

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
	if n := extractActiveFromEvents(log); n >= 0 {
		return n, true
	}
	if n := extractActiveFromStatsLines(log); n >= 0 {
		return n, true
	}
	return 0, false
}

func extractActiveFromEvents(log string) int {
	const prefix = "__WDTT_EVENT__|STATS|"
	last := -1
	for _, line := range strings.Split(log, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		payload := strings.TrimPrefix(line, prefix)
		var data struct {
			Active int `json:"active"`
		}
		if err := json.Unmarshal([]byte(payload), &data); err != nil {
			continue
		}
		if data.Active >= 0 {
			last = data.Active
		}
	}
	return last
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
