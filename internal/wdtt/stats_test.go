package wdtt

import (
	"strconv"
	"strings"
	"testing"
)

const sampleStatsLog = `
2026/07/23 10:15:15 [СТАТИСТИКА] Активных: 9 | Трафик: 0.00 МБ
__WDTT_EVENT__|STATS|{"active":9,"bytes_down":0,"bytes_up":0}
2026/07/23 10:15:21 [СТАТИСТИКА] Активных: 18 | Трафик: 0.00 МБ
__WDTT_EVENT__|STATS|{"active":18,"bytes_down":0,"bytes_up":0}
`

func TestExtractActiveConnectionsFromLog(t *testing.T) {
	if got := ExtractActiveConnectionsFromLog(sampleStatsLog); got != 18 {
		t.Fatalf("got=%d want=18", got)
	}
	if got := ExtractActiveConnectionsFromLog(""); got != 0 {
		t.Fatalf("empty log got=%d", got)
	}
}

// statsLog собирает окно STATS-событий: вверх всегда +148 Б за сэмпл
// (keepalive), вниз — по шагу down.
func statsLog(n int, down int64) string {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteString(`__WDTT_EVENT__|STATS|{"active":9,"bytes_down":`)
		sb.WriteString(strconv.FormatInt(2424+int64(i)*down, 10))
		sb.WriteString(`,"bytes_up":`)
		sb.WriteString(strconv.Itoa(52000 + i*148))
		sb.WriteString("}\n")
	}
	return sb.String()
}

func stalledStatsLog() string { return statsLog(stallMinEvents, 0) }

func TestTrafficStalled(t *testing.T) {
	if !trafficStalled(statsEvents(stalledStatsLog())) {
		t.Fatal("expected stalled zombie relay")
	}

	// Полное окно, но вниз идёт трафик — это рабочий клиент.
	if trafficStalled(statsEvents(statsLog(stallMinEvents, 1200))) {
		t.Fatal("растущий bytes_down — рестарта быть не должно")
	}

	// Короткий хвост лога: судить не по чему.
	if trafficStalled(statsEvents(statsLog(stallMinEvents-1, 0))) {
		t.Fatal("окна не хватает — вердикта быть не должно")
	}

	var flat strings.Builder
	for i := 0; i < stallMinEvents; i++ {
		flat.WriteString(`__WDTT_EVENT__|STATS|{"active":9,"bytes_down":100,"bytes_up":100}` + "\n")
	}
	if trafficStalled(statsEvents(flat.String())) {
		t.Fatal("idle link with flat up/down must not restart")
	}
}
