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
//
// Сторож СЕМАНТИКИ, не ранней отсечки: со снятым гейтом он останется зелёным,
// потому что строки всё равно дойдут до AppLog. Ранняя отсечка проверяется
// тестом выше, где фейковый логгер порога не применяет.
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
		"info": logging.LevelInfo,
		// trace подробнее debug у движка, поэтому оба идут в самый подробный
		// НАШ уровень. Прежнее trace→LevelFull переворачивало подробность.
		"debug": logging.LevelDebug, "trace": logging.LevelDebug,
		// Неизвестный тип не прячем: движок умеет отдать "unknown", и при
		// заводском пороге info самый подробный уровень означал бы «скрыть».
		"unknown": logging.LevelWarn, "": logging.LevelWarn,
		"  INFO  ": logging.LevelInfo,
	}
	for in, want := range cases {
		if got := levelForClashType(in); got != want {
			t.Errorf("levelForClashType(%q) = %q, ожидалось %q", strings.TrimSpace(in), got, want)
		}
	}
}

// Просим у движка ровно нужный уровень: `?level=` фильтрует на ЕГО стороне до
// сериализации в JSON и записи в сокет, то есть отсечённая строка не пересекает
// сокет и не требует Unmarshal у нас. Отсев в forward это не заменяет — он
// закрывает промежуток до ближайшего переподключения.
func TestLogForwarder_AsksEngineForNeededLevelOnly(t *testing.T) {
	cases := []struct {
		configured logging.Level
		want       string
	}{
		{logging.LevelDebug, "trace"}, // нужны и debug, и trace движка
		{logging.LevelFull, "info"},   // подробнее info из движка ничего не видно
		{logging.LevelInfo, "info"},
		{logging.LevelWarn, "warn"},
		{logging.LevelError, "warn"}, // error и warn проходят при любом пороге
	}
	for _, c := range cases {
		f := NewLogForwarder(func() string { return "unused" }, &gateLogger{configured: c.configured})
		if got := f.desiredClashLevel(); got != c.want {
			t.Errorf("порог %q: просим у движка %q, ожидалось %q", c.configured, got, c.want)
		}
	}
}

// Логгер без LevelGate — работаем как раньше: просим всё и отсеиваем сами
// (точнее, не отсеиваем вовсе, это делает AppLog).
func TestLogForwarder_WithoutGateAsksForTrace(t *testing.T) {
	f := NewLogForwarder(func() string { return "unused" }, &captureLogger{})
	if f.gate != nil {
		t.Fatal("captureLogger не должен реализовывать LevelGate — тест проверяет не то")
	}
	if got := f.desiredClashLevel(); got != "trace" {
		t.Errorf("без LevelGate просим %q, ожидалось trace", got)
	}
}

// Уровень, запрошенный у движка, обязан соответствовать тому, что мы потом
// пропускаем. Разойдясь, они дали бы потерю: движок не прислал бы строку,
// которую наш порог пропустил бы.
func TestDesiredLevelCoversEverythingWePass(t *testing.T) {
	// Уровни движка в порядке возрастания подробности и соответствующий им
	// наш уровень (levelForClashType).
	engine := []struct {
		clash string
		ours  logging.Level
	}{
		{"error", logging.LevelError}, {"warn", logging.LevelWarn},
		{"info", logging.LevelInfo}, {"debug", logging.LevelDebug}, {"trace", logging.LevelDebug},
	}
	rank := map[string]int{"warn": 0, "info": 1, "debug": 2, "trace": 3}

	for _, configured := range []logging.Level{
		logging.LevelError, logging.LevelWarn, logging.LevelInfo, logging.LevelFull, logging.LevelDebug,
	} {
		lg := &gateLogger{configured: configured}
		f := NewLogForwarder(func() string { return "unused" }, lg)
		asked := f.desiredClashLevel()

		for _, e := range engine {
			if !logging.IsVisible(e.ours, configured) {
				continue // эту строку мы бы и так отбросили
			}
			if rank[e.clash] > rank[asked] {
				t.Errorf("порог %q: пропустили бы %q, но просим у движка только %q — строка не придёт",
					configured, e.clash, asked)
			}
		}
	}
}
