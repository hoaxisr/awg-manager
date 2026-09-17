package singbox

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hoaxisr/awg-manager/internal/logging"
)

// gateLogger изображает логгер с настроенным порогом.
//
// AppLog считает ВСЁ, что до него дошло, и порог не применяет — сознательно:
// иначе тест не отличил бы раннюю отсечку от отсечки внутри AppLog и был бы
// зелёным при снятом гейте (проверено мутацией).
type gateLogger struct {
	configured logging.Level
	written    atomic.Int64
}

func (g *gateLogger) AppLog(logging.Level, string, string, string, string, string) {
	g.written.Add(1)
}

func (g *gateLogger) Visible(level logging.Level) bool {
	return logging.IsVisible(level, g.configured)
}

// Строка, отсеянная по уровню, не должна доходить до разбора: движок на уровне
// `info` пишет строку на соединение и на DNS-запрос, а classifyPayload гоняет
// по каждой регулярки. Раньше вся эта работа делалась и выбрасывалась уже
// внутри AppLog.
func TestLogForwarder_DropsBelowConfiguredLevelBeforeParsing(t *testing.T) {
	lg := &gateLogger{configured: logging.LevelInfo}
	f := NewLogForwarder(func() string { return "unused" }, lg)
	if f.gate == nil {
		t.Fatal("LevelGate не подхвачен — ранняя отсечка не работает")
	}

	// debug ниже порога info
	f.forward([]byte(`{"type":"debug","payload":"inbound/tcp: connection from 192.168.1.5"}`))
	if got := lg.written.Load(); got != 0 {
		t.Errorf("записей %d, ожидалось 0: debug ниже настроенного info", got)
	}

	// info проходит
	f.forward([]byte(`{"type":"info","payload":"inbound/tcp: started"}`))
	if got := lg.written.Load(); got != 1 {
		t.Errorf("записей %d, ожидалась 1", got)
	}
}

// Error и Warn обязаны проходить при ЛЮБОМ настроенном уровне — это правило
// IsVisible, и ранняя отсечка не смеет его нарушить.
func TestLogForwarder_ErrorAndWarnAlwaysPass(t *testing.T) {
	lg := &gateLogger{configured: logging.LevelError} // самый строгий
	f := NewLogForwarder(func() string { return "unused" }, lg)

	for _, typ := range []string{"error", "fatal", "panic", "warn", "warning"} {
		f.forward([]byte(`{"type":"` + typ + `","payload":"dns: upstream failed"}`))
	}
	if got := lg.written.Load(); got != 5 {
		t.Errorf("записей %d, ожидалось 5: error/warn проходят при любом пороге", got)
	}
}

// Соответствие «тип строки движка → уровень» живёт в ОДНОМ месте и
// используется и проверкой, и записью. Разойдясь, они дали бы худший исход:
// строку, отсеянную проверкой, но нужную пользователю.
func TestLevelForClashType(t *testing.T) {
	cases := map[string]logging.Level{
		"error": logging.LevelError, "fatal": logging.LevelError, "panic": logging.LevelError,
		"warn": logging.LevelWarn, "warning": logging.LevelWarn,
		"info": logging.LevelInfo, "debug": logging.LevelDebug,
		"trace": logging.LevelFull, "": logging.LevelFull, "  INFO  ": logging.LevelInfo,
	}
	for in, want := range cases {
		if got := levelForClashType(in); got != want {
			t.Errorf("levelForClashType(%q) = %q, ожидалось %q", strings.TrimSpace(in), got, want)
		}
	}
}
