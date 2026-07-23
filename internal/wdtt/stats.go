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
	if n := extractActiveFromEvents(log); n >= 0 {
		return n
	}
	return extractActiveFromStatsLines(log)
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
	if last < 0 {
		return 0
	}
	return last
}
