package wdtt

import (
	"strings"
	"testing"
)

// TestDrainCONFIGSurvivesEviction проверяет, что CONFIG-событие, пойманное в
// drain, остаётся доступным через Status() даже после того как исходная строка
// вытеснена из ring-буфера логов последующими STATS-строками.
func TestDrainCONFIGSurvivesEviction(t *testing.T) {
	p := newProcess("test", "", t.TempDir())

	var sb strings.Builder
	sb.WriteString(`__WDTT_EVENT__|CONFIG|{"config":"[Interface]\nPrivateKey = k\nAddress = 10.0.0.2/32\n\n[Peer]\nPublicKey = pk\nAllowedIPs = 0.0.0.0/0\n"}`)
	sb.WriteByte('\n')
	// Вытесняем CONFIG за пределы ring-буфера (processLogMaxLines=500).
	for i := 0; i < processLogMaxLines+100; i++ {
		sb.WriteString("__WDTT_EVENT__|STATS|{}\n")
	}

	p.drain(strings.NewReader(sb.String()))

	// CONFIG должен быть вытеснен из лога...
	if got := ExtractWGConfigFromLog(p.logTail.String()); got != "" {
		t.Fatalf("expected CONFIG evicted from log, got %q", got)
	}
	// ...но сохранён в поле процесса.
	if !strings.Contains(p.lastWgConfig, "PublicKey = pk") {
		t.Fatalf("lastWgConfig not retained: %q", p.lastWgConfig)
	}
	if got := p.Status().WgConfig; !strings.Contains(got, "PublicKey = pk") {
		t.Fatalf("Status().WgConfig=%q", got)
	}
}
