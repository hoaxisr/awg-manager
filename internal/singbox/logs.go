package singbox

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/logging"
)

type LogForwarder struct {
	// clashAddr — ПОСТАВЩИК адреса, а не снимок: порт Clash API меняется в
	// рантайме (issue #788), а Run переподключается каждые reconnect секунд,
	// так что новый адрес подхватывается сам, без перезапуска горутины.
	clashAddr func() string

	// gate — необязательная способность логгера сказать, попадёт ли запись
	// такого уровня в журнал. Есть — отсеиваем ДО разбора; нет — работаем
	// как раньше.
	gate logging.LevelGate

	inbound  *logging.ScopedLogger
	outbound *logging.ScopedLogger
	dns      *logging.ScopedLogger
	router   *logging.ScopedLogger
	runtime  *logging.ScopedLogger

	http *http.Client

	reconnect time.Duration
}

// levelWatchInterval — как часто сверять запрошенный у движка уровень с
// нужным. Смена уровня журнала — редкое ручное действие, задержка до
// levelWatchInterval + reconnect приемлема.
const levelWatchInterval = 5 * time.Second

func NewLogForwarder(clashAddr func() string, appLogger logging.AppLogger) *LogForwarder {
	gate, _ := appLogger.(logging.LevelGate)
	return &LogForwarder{
		clashAddr: clashAddr,
		gate:      gate,
		inbound:   logging.NewScopedLogger(appLogger, logging.GroupSingbox, logging.SubSBInbound),
		outbound:  logging.NewScopedLogger(appLogger, logging.GroupSingbox, logging.SubSBOutbound),
		dns:       logging.NewScopedLogger(appLogger, logging.GroupSingbox, logging.SubSBDNS),
		router:    logging.NewScopedLogger(appLogger, logging.GroupSingbox, logging.SubSBRouter),
		runtime:   logging.NewScopedLogger(appLogger, logging.GroupSingbox, logging.SubSBRuntime),
		http:      &http.Client{},
		reconnect: 3 * time.Second,
	}
}

func (f *LogForwarder) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		f.runOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(f.reconnect):
		}
	}
}

func (f *LogForwarder) runOnce(ctx context.Context) {
	level := f.desiredClashLevel()

	// Поток живёт, пока жив sing-box, и сам по себе новый уровень не
	// подхватит: пользователь поднял бы подробность ради диагностики и не
	// увидел бы ничего нового. Сторож рвёт соединение на смене — Run
	// переподключится с новым `?level=`.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		t := time.NewTicker(levelWatchInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if f.desiredClashLevel() != level {
					cancel()
					return
				}
			}
		}
	}()

	url := fmt.Sprintf("http://%s/logs?level=%s", f.clashAddr(), level)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return
	}
	resp, err := f.http.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		f.forward(sc.Bytes())
	}
}

type clashLogEntry struct {
	Type    string `json:"type"`
	Payload string `json:"payload"`
}

var timestampPrefix = regexp.MustCompile(`^[+\-]\d{4}\s+\d{4}-\d{2}-\d{2}\s+\d{2}:\d{2}:\d{2}(?:\.\d+)?\s+(?:FATAL|ERROR|WARN|INFO|DEBUG|TRACE)\s+`)

var connIDPrefix = regexp.MustCompile(`^\[\d+\s+[\d.]+[a-zµ]+\]\s+`)

var contextBracket = regexp.MustCompile(`\[[^\]]*\]`)

func classifyPayload(payload string) (subgroup, target, message string) {
	msg := timestampPrefix.ReplaceAllString(payload, "")
	msg = connIDPrefix.ReplaceAllString(msg, "")
	msg = strings.TrimSpace(msg)

	head, rest, hasSep := cutSegment(msg)
	if !hasSep {
		return logging.SubSBRuntime, "sing-box", msg
	}

	category, tag := splitCategory(head)
	switch category {
	case "inbound":
		return logging.SubSBInbound, tagOr(tag, "inbound"), rest
	case "outbound":
		return logging.SubSBOutbound, tagOr(tag, "outbound"), rest
	case "dns":
		return logging.SubSBDNS, tagOr(tag, "dns"), rest
	case "router", "route":
		return logging.SubSBRouter, tagOr(tag, "router"), rest
	default:
		return logging.SubSBRuntime, tagOr(tag, "sing-box"), msg
	}
}

func cutSegment(msg string) (head, rest string, ok bool) {
	idx := strings.Index(msg, ": ")
	if idx < 0 {
		return msg, "", false
	}
	return msg[:idx], strings.TrimSpace(msg[idx+2:]), true
}

func splitCategory(head string) (category, tag string) {
	if br := contextBracket.FindStringIndex(head); br != nil {
		tag = strings.TrimSpace(head[br[0]+1 : br[1]-1])
		head = strings.TrimSpace(head[:br[0]])
	}
	if i := strings.IndexAny(head, "/ "); i >= 0 {
		return head[:i], tag
	}
	return head, tag
}

func tagOr(tag, fallback string) string {
	if tag == "" {
		return fallback
	}
	return tag
}

func (f *LogForwarder) forward(line []byte) {
	if len(line) == 0 {
		return
	}
	var e clashLogEntry
	if err := json.Unmarshal(line, &e); err != nil {
		return
	}
	payload := strings.TrimSpace(e.Payload)
	if payload == "" {
		return
	}
	// Уровень проверяем ДО разбора: classifyPayload гоняет по строке
	// регулярки, а движок на уровне `info` пишет строку на соединение и на
	// DNS-запрос. Раньше вся эта работа делалась и выбрасывалась уже внутри
	// AppLog. Семантика та же: Visible — ровно та проверка, что стоит там
	// первой (Error и Warn проходят при любом настроенном уровне).
	level := levelForClashType(e.Type)
	if f.gate != nil && !f.gate.Visible(level) {
		return
	}

	subgroup, target, message := classifyPayload(payload)
	scoped := f.scopedFor(subgroup)
	if scoped == nil {
		return
	}
	scoped.At(level, "run", target, message)
}

// levelForClashType переводит тип строки движка в наш уровень.
//
// ОДНА точка соответствия на проверку и на запись: разойдясь, они дали бы
// худший из возможных исходов — строку, отсеянную проверкой, но нужную
// пользователю.
//
// `trace` идёт в LevelDebug вместе с `debug`, а НЕ в LevelFull, как было
// раньше. Прежнее отображение переворачивало подробность: у движка trace
// подробнее debug, а в нашей шкале LevelFull (приоритет 2) МЕНЕЕ подробен, чем
// LevelDebug (3) — то есть при пороге «full» пользователь видел trace-строки
// движка и НЕ видел его же debug-строки.
//
// Неизвестный тип — LevelWarn, а не самый подробный уровень. Движок умеет
// отдать "unknown", и любой новый ярлык будущей версии попадёт сюда же;
// прятать такую строку при заводских настройках (порог info) — fail-closed
// там, где журнал существует ради разбора аварий.
func levelForClashType(clashType string) logging.Level {
	switch strings.ToLower(strings.TrimSpace(clashType)) {
	case "error", "fatal", "panic":
		return logging.LevelError
	case "warn", "warning":
		return logging.LevelWarn
	case "info":
		return logging.LevelInfo
	case "debug", "trace":
		return logging.LevelDebug
	default:
		return logging.LevelWarn
	}
}

// desiredClashLevel — самый подробный уровень, который сейчас нужен журналу, в
// терминах движка.
//
// Просить у движка ровно нужное дешевле любого нашего отсева: `?level=`
// фильтрует на ЕГО стороне до сериализации в JSON и записи в сокет
// (experimental/clashapi/server.go), то есть строка не пересекает сокет и не
// требует Unmarshal у нас. Отсев в forward при этом не лишний: он закрывает
// промежуток до ближайшего переподключения и работает, если логгер LevelGate
// не поддержал.
func (f *LogForwarder) desiredClashLevel() string {
	if f.gate == nil {
		return "trace"
	}
	switch {
	case f.gate.Visible(logging.LevelDebug):
		return "trace"
	case f.gate.Visible(logging.LevelInfo):
		return "info"
	default:
		// error и warn проходят при любом пороге (IsVisible).
		return "warn"
	}
}

func (f *LogForwarder) scopedFor(subgroup string) *logging.ScopedLogger {
	switch subgroup {
	case logging.SubSBInbound:
		return f.inbound
	case logging.SubSBOutbound:
		return f.outbound
	case logging.SubSBDNS:
		return f.dns
	case logging.SubSBRouter:
		return f.router
	default:
		return f.runtime
	}
}
