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

func TestClientTrafficStalled(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < stallMinEvents; i++ {
		up := 52000 + i*148
		sb.WriteString("__WDTT_EVENT__|STATS|{\"active\":9,\"bytes_down\":2424,\"bytes_up\":")
		sb.WriteString(strconv.Itoa(up))
		sb.WriteString("}\n")
	}
	if !ClientTrafficStalled(sb.String()) {
		t.Fatal("expected stalled zombie relay")
	}

	healthy := sampleStatsLog
	if ClientTrafficStalled(healthy) {
		t.Fatal("expected healthy traffic not stalled")
	}

	var flat strings.Builder
	for i := 0; i < stallMinEvents; i++ {
		flat.WriteString("__WDTT_EVENT__|STATS|{\"active\":9,\"bytes_down\":100,\"bytes_up\":100}\n")
	}
	if ClientTrafficStalled(flat.String()) {
		t.Fatal("idle link with flat up/down must not restart")
	}
}
