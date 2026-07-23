package wdtt

import "testing"

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
