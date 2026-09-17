package singbox

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

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
		// Монотонно по подробности: info < debug < trace у движка ложится на
		// info < full < debug у нас. Оба варианта, которые НЕ монотонны, дают
		// потерю: trace→Full переворачивает подробность, а debug+trace→Debug
		// делает порог «полный» тождественным «info» для строк движка.
		"debug": logging.LevelFull, "trace": logging.LevelDebug,
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
		{logging.LevelFull, "debug"},  // trace на этом пороге не показываем
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

// Отображение уровней обязано быть МОНОТОННЫМ: чем подробнее строка у движка,
// тем подробнее наш уровень. Нарушение монотонности — не абстракция, а потеря
// содержимого: при пороге «полный» пользователь видел бы trace и не видел
// debug того же движка (так было до 17.09).
func TestLevelMappingIsMonotonic(t *testing.T) {
	// Уровни движка от менее подробного к более подробному.
	engine := []string{"info", "debug", "trace"}
	prio := map[logging.Level]int{
		logging.LevelInfo: 1, logging.LevelFull: 2, logging.LevelDebug: 3,
	}
	prev := 0
	for _, e := range engine {
		got := prio[levelForClashType(e)]
		if got == 0 {
			t.Fatalf("%q отображается в уровень вне шкалы подробности: %q", e, levelForClashType(e))
		}
		if got <= prev {
			t.Errorf("%q даёт приоритет %d, а предыдущий уровень движка — %d: подробность не возрастает",
				e, got, prev)
		}
		prev = got
	}
}

// Порог «полный» обязан показывать из движка БОЛЬШЕ, чем «info». Иначе опция в
// настройках для строк sing-box мертва.
func TestFullShowsMoreThanInfo(t *testing.T) {
	var atInfo, atFull int
	for _, e := range []string{"info", "debug", "trace"} {
		lvl := levelForClashType(e)
		if logging.IsVisible(lvl, logging.LevelInfo) {
			atInfo++
		}
		if logging.IsVisible(lvl, logging.LevelFull) {
			atFull++
		}
	}
	if atFull <= atInfo {
		t.Errorf("на пороге «полный» видно %d уровней движка, на «info» — %d: опция бесполезна", atFull, atInfo)
	}
}

// Подсказка в настройках («debug-строки — с FULL, trace — только с DEBUG»,
// LoggingSettings.svelte) — обещание пользователю. Ни монотонность, ни
// «FULL подробнее INFO» его не удержат: попроси мы на пороге FULL сразу
// "trace", оба теста остались бы зелёными, а подсказка стала бы ложью.
func TestAskedLevelMatchesSettingsHint(t *testing.T) {
	cases := []struct {
		threshold logging.Level
		want      string
	}{
		{logging.LevelInfo, "info"},
		{logging.LevelFull, "debug"},
		{logging.LevelDebug, "trace"},
	}
	for _, c := range cases {
		f := NewLogForwarder(func() string { return "unused" }, &gateLogger{configured: c.threshold})
		if got := f.desiredClashLevel(); got != c.want {
			t.Errorf("порог %v: просим у движка %q, подсказка обещает %q", c.threshold, got, c.want)
		}
	}
}

// Журнал выключен целиком — поток к движку не открываем вовсе. Держать его,
// принимать каждую строку и выбрасывать её после разбора — ровно та работа,
// которую эта линия правок убирает.
func TestLogForwarder_DisabledLoggingAsksForNothing(t *testing.T) {
	// gateLogger с порогом, при котором Visible ложен для ВСЕХ уровней,
	// изображает выключенный журнал: ровно так ведёт себя logging.Service,
	// когда IsEnabled() == false.
	f := NewLogForwarder(func() string { return "unused" }, &disabledLogger{})
	if got := f.desiredClashLevel(); got != "" {
		t.Errorf("при выключенном журнале просим %q, ожидали пусто", got)
	}
}

type disabledLogger struct{}

func (d *disabledLogger) AppLog(logging.Level, string, string, string, string, string) {}
func (d *disabledLogger) Visible(logging.Level) bool                                   { return false }

// mutableGate — порог, который можно менять на ходу, как это делает
// пользователь в настройках.
type mutableGate struct {
	mu         sync.Mutex
	configured logging.Level
}

func (m *mutableGate) AppLog(logging.Level, string, string, string, string, string) {}
func (m *mutableGate) Visible(l logging.Level) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return logging.IsVisible(l, m.configured)
}
func (m *mutableGate) set(l logging.Level) {
	m.mu.Lock()
	m.configured = l
	m.mu.Unlock()
}

// Поток к движку обязан открываться с нужным `?level=`, иначе вся экономия
// (движок не сериализует и не шлёт лишнее) не работает, а проверить это по
// desiredClashLevel нельзя — тот URL не собирает.
func TestLogForwarder_RequestCarriesLevel(t *testing.T) {
	got := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case got <- r.URL.Query().Get("level"):
		default:
		}
		<-r.Context().Done() // держим поток открытым
	}))
	defer srv.Close()

	gate := &mutableGate{configured: logging.LevelInfo}
	f := NewLogForwarder(func() string { return strings.TrimPrefix(srv.URL, "http://") }, gate)
	f.levelWatch = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	go f.Run(ctx)
	defer cancel()

	select {
	case lvl := <-got:
		if lvl != "info" {
			t.Fatalf("запросили level=%q, ожидали info", lvl)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("форвардер не подключился")
	}

	// Пользователь поднял подробность — поток обязан переоткрыться с новым
	// уровнем, иначе он живёт со старым, пока жив sing-box.
	gate.set(logging.LevelDebug)
	deadline := time.After(5 * time.Second)
	for {
		select {
		case lvl := <-got:
			if lvl == "trace" {
				return // переоткрылся с нужным уровнем
			}
		case <-deadline:
			t.Fatal("после смены порога поток не переоткрылся с новым уровнем")
		}
	}
}

// Ранний выход при выключенном журнале обязан закрывать САМ ПОТОК, а не только
// вычисление уровня: без него форвардер уходил бы на `GET /logs?level=` с пустым
// уровнем. Проверка desiredClashLevel() этого не ловит (найдено ревью 17.09).
func TestLogForwarder_DisabledLoggingOpensNoStream(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-r.Context().Done()
	}))
	defer srv.Close()

	f := NewLogForwarder(func() string { return strings.TrimPrefix(srv.URL, "http://") },
		&disabledLogger{})
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	f.Run(ctx)

	if n := hits.Load(); n != 0 {
		t.Errorf("при выключенном журнале форвардер сходил к движку %d раз", n)
	}
}

// Горутина-сторож уровня обязана уходить вместе с контекстом: она заводится на
// КАЖДУЮ попытку соединения, а при лежащем sing-box их по одной каждые 3 с.
//
// Считать runtime.NumGoroutine с допуском нельзя: любой допуск проглатывает ровно
// ту одну горутину, ради которой тест написан, — прежняя редакция проходила под
// мутацией «убрать case <-ctx.Done() из селекта сторожа» (найдено ревью 17.09).
// goleak сравнивает поимённо и допуска не имеет.
func TestLogForwarder_WatcherGoroutineStops(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))

	f := NewLogForwarder(func() string { return strings.TrimPrefix(srv.URL, "http://") },
		&gateLogger{configured: logging.LevelInfo})
	f.levelWatch = 10 * time.Millisecond

	ignore := goleak.IgnoreCurrent()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.Run(ctx); close(done) }()

	time.Sleep(300 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run не завершился по отмене контекста")
	}
	srv.Close()

	goleak.VerifyNone(t, ignore)
}
