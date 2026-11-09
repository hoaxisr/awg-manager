package autolist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/directtrack"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/exclusions"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/judge"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/packet"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/packetbridge"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/packetruntime"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/probeq"
)

// Engine связывает наблюдение, очередь проб и правщика правил.
// Один цикл: poll (сборщики) -> queue (пробы с потолками) -> act (правила).
type Engine struct {
	obs    *Observer
	prober *Prober
	// tunnelProber — тестовый seam; production использует тот же curl Prober.
	tunnelProber *Prober
	rules        *RuleClient
	table        *Table
	history      *historyStore

	// probeQueue владеет приоритетами, техническими лимитами и in-flight слотами.
	// probeTasks хранит принадлежащие autolist данные, которые
	// намеренно не входят в probeq.Task: registered domain, observed IP и
	// probe-only происхождение номинации.
	probeQueue *probeq.Queue
	probeWake  chan struct{}

	// packet владеет AF_PACKET/conntrack и единственным judge-owner. pending содержит только
	// уже подтверждённые judge outputs, для которых Clash ещё не показал маршрут.
	packet       *packetruntime.Runtime
	packetWake   chan struct{}
	dispatch     chan packetbridge.Result
	dispatchDrop uint64
	packetMu     sync.Mutex
	packetPollAt time.Time
	pending      map[packet.Flow]pendingPacket
	pendingDrop  uint64
	pendingTTL   uint64
	nonDirect    uint64

	// configMu сериализует config edit+save. writeMu сериализует router RMW;
	// addsMu защищает только короткий часовой счётчик, не сетевой I/O.
	configMu sync.Mutex
	writeMu  sync.Mutex
	addsMu   sync.Mutex

	mu             sync.Mutex
	probeTasks     map[string]probeTask
	domainRevision map[string]uint64
	configRevision uint64
	taskCancels    map[string]context.CancelFunc
	activeDomain   string
	probeNotesDue  bool
	workerBusy     bool
	probingHost    string
	now            func() time.Time
	addsWindow     []time.Time // добавления правил за час (режим auto)

	// cfg — текущая конфигурация (под mu).
	cfg Config
	// cfgPath — файл конфига; пусто — конфиг не сохраняется.
	cfgPath string
	runCtx  context.Context // nil до Run; под mu

	// BaseURL/APIKey — доступ к awg-manager.
	BaseURL string
	APIKey  string

	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

type pendingPacket struct {
	result packetbridge.Result
	at     time.Time
}

// probeTask сохраняет оба ключа autolist: точный host для активной пробы и
// регистрируемый domain для строки таблицы и итогового правила.
type probeTask struct {
	host            string
	domain          string
	ip              string // IP наблюдаемого соединения; пусто — обычный DNS
	why             Why
	source          string
	probeOnly       bool // до подтверждения не создаёт кандидата
	probeInterface  string
	wanURL          string
	byDomain        bool
	domainRevision  uint64
	configRevision  uint64
	targetInterface string
	ctx             context.Context
}

func NewEngine(base, apiKey string) *Engine {
	e := &Engine{
		obs:            NewObserver(base, apiKey),
		prober:         &Prober{TimeoutSec: ProbeTimeoutSec},
		rules:          NewRuleClient(base, apiKey),
		table:          NewTable(),
		history:        newHistoryStore(),
		probeQueue:     probeq.New(),
		probeWake:      make(chan struct{}, 1),
		probeTasks:     map[string]probeTask{},
		domainRevision: map[string]uint64{},
		taskCancels:    map[string]context.CancelFunc{},
		packetWake:     make(chan struct{}, 1),
		dispatch:       make(chan packetbridge.Result, PacketPendingCapacity),
		pending:        map[packet.Flow]pendingPacket{},
		now:            time.Now,
		addsWindow:     make([]time.Time, 0, AddsPerHour),
		stop:           make(chan struct{}),
		done:           make(chan struct{}),
	}
	e.probeQueue.SetEvictionHandler(func(host string) {
		if task, ok := e.probeTasks[host]; ok {
			e.table.ClearQueued(task.domain)
		}
		delete(e.probeTasks, host)
		// Задача не дошла до worker: соответствующие open-события могут быть
		// приняты повторно в пределах ограниченного числа попыток.
		e.obs.releaseOpenHost(host)
	})
	e.obs.instant = func() bool { return e.Config().Instant }
	e.packet = packetruntime.New(packetruntime.Config{
		Discover: e.rules.LANBridges,
		OnResult: e.processPacketResult,
		OnFlowStart: func(context.Context, packet.Flow) {
			e.wakePacketCorrelation()
		},
		Now:     func() time.Time { return e.now() },
		Verbose: func() bool { return e.Config().Verbose },
		NewJudge: func() *judge.Judge {
			if e.Config().Instant {
				return judge.NewWithParams(judge.Params{
					FailThreshold: 1, RetransThreshold: 1, StallNotesThreshold: 1,
				})
			}
			return judge.New()
		},
	})
	return e
}

// Table отдаёт снимок для HTTP-эндпоинта сателлита.
func (e *Engine) Table() *Table { return e.table }

func (e *Engine) cancelDomain(domain string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.domainRevision[domain]++
	for host, task := range e.probeTasks {
		if task.domain == domain {
			e.probeQueue.Cancel(host)
			e.table.ClearQueued(domain)
			delete(e.probeTasks, host)
		}
	}
	if e.activeDomain == domain {
		for _, cancel := range e.taskCancels {
			cancel()
		}
	}
}

func (e *Engine) cancelAllTasks() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for host, task := range e.probeTasks {
		e.probeQueue.Cancel(host)
		e.table.ClearQueued(task.domain)
		delete(e.probeTasks, host)
	}
	for _, cancel := range e.taskCancels {
		cancel()
	}
}

// Revision памяти нужна только пока существует queued/active задача. После её
// завершения запись можно безопасно удалить: старых задач для сравнения уже нет.
func (e *Engine) releaseDomainRevision(domain string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.activeDomain == domain || (e.workerBusy && registeredDomain(e.probingHost) == domain) {
		return
	}
	for _, task := range e.probeTasks {
		if task.domain == domain {
			return
		}
	}
	delete(e.domainRevision, domain)
}

// Dismiss скрывает кандидата по ручному запросу, отменяет ожидающие задачи и
// запрещает автозапись результата уже запущенной пробы.
func (e *Engine) Dismiss(domain string) {
	row, exists := e.table.get(domain)
	now := e.now()
	e.cancelDomain(domain)
	e.table.Dismiss(domain, now)
	e.releaseDomainRevision(domain)
	e.recordHistory([]HistoryEntry{
		historyEntryFromCandidate(domain, "dismissed", "user", now, row, exists),
	})
}

// Forget удаляет кандидата и отменяет незавершённое решение по нему.
func (e *Engine) Forget(domain string) {
	e.cancelDomain(domain)
	e.table.Forget(domain)
	e.releaseDomainRevision(domain)
}

// Config возвращает текущую конфигурацию.
func (e *Engine) Config() Config {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cfg
}

func (e *Engine) debugf(format string, args ...any) {
	if e.Config().Verbose {
		log.Printf("[autolist:debug] "+format, args...)
	}
}

func (e *Engine) recordHistory(entries []HistoryEntry) {
	if err := e.history.record(entries); err != nil {
		log.Printf("[autolist] история не сохранена: %v", err)
	}
}

func writeConfigAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".awgmautolist-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// updateConfig сериализует read-modify-write, атомарно сохраняет файл и только
// затем меняет память. Router writeMu здесь намеренно не используется.
func (e *Engine) updateConfig(edit func(*Config) error) error {
	e.configMu.Lock()
	defer e.configMu.Unlock()
	cfg := e.Config()
	if err := edit(&cfg); err != nil {
		return err
	}
	if strings.ContainsAny(cfg.ProbeInterface, " \t\n") {
		return errors.New("probeInterface: некорректный интерфейс")
	}
	if strings.ContainsAny(cfg.TargetInterface, " \t\n") {
		return errors.New("targetInterface: некорректный интерфейс")
	}
	if cfg.AutoAdd && !cfg.TunnelProbe {
		return errors.New("автодобавление требует включённой tunnel-пробы")
	}
	if cfg.AutoAdd && cfg.TargetInterface == "" {
		return errors.New("автодобавление требует выбора интерфейса")
	}
	if raw := strings.TrimSpace(cfg.WANCheckURL); raw != "" {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return errors.New("wanCheckUrl: нужен полный http(s) URL")
		}
	}
	e.mu.Lock()
	path := e.cfgPath
	runCtx := e.runCtx
	e.mu.Unlock()
	if path != "" {
		b, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			return err
		}
		if err := writeConfigAtomic(path, append(b, '\n')); err != nil {
			return err
		}
	}
	e.mu.Lock()
	previous := e.cfg
	autoChanged := previous.AutoAdd != cfg.AutoAdd || previous.TargetInterface != cfg.TargetInterface || previous.TunnelProbe != cfg.TunnelProbe
	if autoChanged {
		e.configRevision++
	}
	e.cfg = cfg
	e.mu.Unlock()
	if autoChanged {
		e.cancelAllTasks()
	}
	if runCtx != nil && previous.PacketEnabled && cfg.PacketEnabled && previous.Instant != cfg.Instant {
		e.stopPacket()
	}
	if !cfg.PacketEnabled {
		e.stopPacket()
	} else if runCtx != nil {
		e.startPacket(runCtx)
	}
	return nil
}

// SetConfig заменяет конфигурацию для прямых вызовов и тестов.
func (e *Engine) SetConfig(cfg Config) error {
	return e.updateConfig(func(current *Config) error {
		*current = cfg
		return nil
	})
}

// PatchConfig применяет JSON merge: omitted поля сохраняют текущее значение,
// явные false/[] применяются. Неизвестные поля по-прежнему игнорируются.
func (e *Engine) PatchConfig(raw []byte) error {
	return e.updateConfig(func(current *Config) error {
		base, err := json.Marshal(current)
		if err != nil {
			return err
		}
		fields := map[string]json.RawMessage{}
		if err := json.Unmarshal(base, &fields); err != nil {
			return err
		}
		patch := map[string]json.RawMessage{}
		if err := json.Unmarshal(raw, &patch); err != nil {
			return err
		}
		for key, value := range patch {
			fields[key] = value
		}
		merged, err := json.Marshal(fields)
		if err != nil {
			return err
		}
		var next Config
		if err := json.Unmarshal(merged, &next); err != nil {
			return err
		}
		*current = next
		return nil
	})
}

// SetVerboseRuntime включает подробный журнал без записи конфигурации на flash.
func (e *Engine) SetVerboseRuntime(enabled bool) {
	e.mu.Lock()
	e.cfg.Verbose = enabled
	e.mu.Unlock()
}

// LoadConfig читает конфиг с диска и применяет. Отсутствие файла — не
// ошибка (первый запуск).
func (e *Engine) LoadConfig(path string) error {
	e.mu.Lock()
	e.cfgPath = path
	e.mu.Unlock()
	historyPath := historyPathFromConfig(path)
	if err := e.history.load(historyPath); err != nil {
		// История — вспомогательные данные: повреждённый журнал не должен
		// мешать загрузке или перезаписи основной конфигурации.
		log.Printf("[autolist] история %s не загружена: %v", historyPath, err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return fmt.Errorf("конфиг %s: %w", path, err)
	}
	// Legacy autoAdd без полноценной interface-пробы не должен работать после
	// обновления. Файл не перезаписываем до явного сохранения пользователем.
	if !cfg.TunnelProbe || cfg.TargetInterface == "" {
		cfg.AutoAdd = false
	}
	e.mu.Lock()
	e.cfg = cfg
	e.mu.Unlock()
	return nil
}

// Run запускает цикл; блокируется до Stop. Наблюдение и очередь проб имеют
// разные периоды: 15 с для срезов Clash API и 120 с для отложенных подозрений.
func (e *Engine) Run(ctx context.Context) {
	e.mu.Lock()
	e.runCtx = ctx
	packetEnabled := e.cfg.PacketEnabled
	e.mu.Unlock()
	workerCtx, stopWorker := context.WithCancel(ctx)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		e.probeWorker(workerCtx)
	}()
	go func() {
		defer workers.Done()
		e.packetCorrelationWorker(workerCtx)
	}()
	defer func() {
		// done означает полное завершение: активный curl, оба worker и packet
		// runtime уже остановлены, а не только основной цикл вышел из select.
		stopWorker()
		workers.Wait()
		e.stopPacket()
		e.mu.Lock()
		e.runCtx = nil
		e.mu.Unlock()
		close(e.done)
	}()
	if packetEnabled {
		e.startPacket(ctx)
	}

	observeTicker := time.NewTicker(PollIntervalSec * time.Second)
	probeTicker := time.NewTicker(ProbeTickSec * time.Second)
	defer observeTicker.Stop()
	defer probeTicker.Stop()
	// Наблюдение начинается сразу; autolist-пробы ждут свой отдельный тик.
	e.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.stop:
			return
		case <-observeTicker.C:
			e.tick(ctx)
		case <-probeTicker.C:
			e.kickProbe(true)
		}
	}
}

func (e *Engine) Stop() {
	e.stopOnce.Do(func() { close(e.stop) })
	<-e.done
}

func (e *Engine) startPacket(ctx context.Context) {
	if e.packet.Snapshot().Running {
		return
	}
	if err := e.packet.Start(ctx); err != nil {
		log.Printf("[autolist] пакетный режим недоступен, продолжаются Clash/log стратегии: %v", err)
	}
}

func (e *Engine) stopPacket() {
	if e.packet == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := e.packet.Stop(ctx); err != nil {
		log.Printf("[autolist] остановка пакетного режима: %v", err)
	}
}

// excluded применяет ручные и rule-set исключения до любых номинаций. Ошибка
// выбранного rule-set запрещает кандидата: нельзя молча потерять исключение.
func (e *Engine) excluded(ctx context.Context, host, domain string) (bool, error) {
	e.mu.Lock()
	cfg := e.cfg
	domains := append([]string(nil), cfg.ExcludeDomains...)
	tags := append([]string(nil), cfg.ExcludeRuleSets...)
	e.mu.Unlock()
	if len(domains) == 0 && len(tags) == 0 {
		return false, nil
	}
	matcher := exclusions.New(exclusions.Config{Domains: domains, RuleSetTags: tags},
		exclusions.RuleSetMatcherFunc(e.matchExcludedRuleSets))
	return matcher.Excluded(ctx, host, domain)
}

func (e *Engine) matchExcludedRuleSets(ctx context.Context, input string, tags []string) (bool, error) {
	results, err := e.rules.MatchRuleSets(ctx, input, tags)
	if err != nil {
		return false, err
	}
	if len(results) != len(tags) {
		return false, fmt.Errorf("rule-set match returned %d results for %d tags", len(results), len(tags))
	}
	matched := false
	seen := make(map[string]bool, len(results))
	for _, result := range results {
		if result.Error != "" {
			return false, errors.New(result.Error)
		}
		if !result.Supported {
			return false, fmt.Errorf("rule-set %q is unsupported", result.Tag)
		}
		if seen[result.Tag] {
			return false, fmt.Errorf("rule-set %q returned more than once", result.Tag)
		}
		seen[result.Tag] = true
		matched = matched || result.Matched
	}
	for _, tag := range tags {
		if !seen[tag] {
			return false, fmt.Errorf("rule-set %q has no match result", tag)
		}
	}
	return matched, nil
}

// coverageAllows проверяет исходный хост и его регистрируемый домен: правило
// могло быть написано на любой из них. Возвращает домен-ключ таблицы.
func (e *Engine) coverageAllows(ctx context.Context, h Host) (string, bool) {
	return e.coverageAllowsDomain(ctx, h, registeredDomain(h.Name), false)
}

func (e *Engine) coverageAllowsDomain(ctx context.Context, h Host, domain string, inspectObservedIP bool) (string, bool) {
	domain, allowed, _ := e.coverageAllowsDomainStatus(ctx, h, domain, inspectObservedIP)
	return domain, allowed
}

// coverageAllowsDomainStatus дополнительно отличает временную ошибку подготовки
// от окончательного отказа. Retry нужен только open-событию, которое ещё не
// принято в очередь; обычные номинации сохраняют прежний fail-closed lifecycle.
func (e *Engine) coverageAllowsDomainStatus(ctx context.Context, h Host, domain string, inspectObservedIP bool) (string, bool, bool) {
	if domain == "" {
		return "", false, false
	}
	excluded, err := e.excluded(ctx, h.Name, domain)
	if err != nil {
		log.Printf("[autolist] %s: исключение не проверено, кандидат отклонён: %v", h.Name, err)
		return "", false, true
	}
	if excluded {
		e.table.Forget(domain)
		return "", false, false
	}
	e.mu.Lock()
	resolveVia := e.cfg.ResolveVia
	resolveIP := e.cfg.ResolveIPOn()
	e.mu.Unlock()

	names := []string{h.Name}
	if domain != h.Name {
		names = append(names, domain)
	}
	for _, name := range names {
		ins, err := e.rules.Inspect(ctx, name)
		if err != nil {
			// Без проверки покрытия кандидата не заводим.
			log.Printf("[autolist] %s: inspect недоступен (%v), кандидат отклонён", name, err)
			return "", false, true
		}
		if ins.HasRoutingDecision() {
			return "", false, false
		}
	}

	// При включённой IP-опции пакетный результат проверяется и по точному
	// наблюдаемому серверу. При выключенной опции не делаем даже этот Inspect.
	if resolveIP && inspectObservedIP && h.IP != "" && !privateIP(h.IP) {
		ins, err := e.rules.Inspect(ctx, h.IP)
		if err != nil {
			log.Printf("[autolist] %s: inspect наблюдаемого IP недоступен (%v), кандидат отклонён", h.IP, err)
			return "", false, true
		}
		if ins.HasRoutingDecision() {
			return "", false, false
		}
	}

	// IP исходного хоста может попасть в ip_cidr или geoip даже без
	// доменного правила. Резолвим именно его, а не другой корневой хост.
	if resolveIP {
		ips, err := e.rules.ResolveDomain(ctx, h.Name, resolveVia)
		if err != nil {
			log.Printf("[autolist] %s: обязательный резолв для IP-покрытия не выполнен: %v", h.Name, err)
			return "", false, true
		}
		for _, ip := range ips {
			ipIns, err := e.rules.Inspect(ctx, ip)
			if err != nil {
				log.Printf("[autolist] %s: inspect адреса %s для IP-покрытия не выполнен: %v", h.Name, ip, err)
				return "", false, true
			}
			if ipIns.HasRoutingDecision() {
				return "", false, false
			}
		}
	}
	return domain, true, false
}

// noteIfUnrouted записывает обычную autolist-номинацию после проверки покрытия.
func (e *Engine) noteIfUnrouted(ctx context.Context, h Host, why Why, source string) bool {
	if _, ok := e.coverageAllows(ctx, h); !ok {
		return false
	}
	if !e.table.Note(h, why, source, e.now()) {
		return false
	}
	return true
}

func (e *Engine) tick(ctx context.Context) {
	connections := e.obs.PollConnections(ctx)
	e.flushPendingPackets(ctx)
	for _, observation := range connections {
		e.debugf("соединение: host=%s ip=%s why=%s probeOnly=%v", observation.Host.Name, observation.Host.IP, observation.Why, observation.ProbeOnly)
		source := "stall"
		if observation.ProbeOnly {
			source = "open"
		}
		task := probeTask{
			host: observation.Host.Name, domain: registeredDomain(observation.Host.Name), ip: observation.Host.IP,
			why: observation.Why, source: source, probeOnly: observation.ProbeOnly,
		}
		if observation.ProbeOnly {
			// Открытое соединение — только повод для пробы, не кандидат. Событие
			// подтверждается после enqueue либо окончательного запрета. Временная
			// подготовка и полная очередь оставляют ограниченный повтор.
			if e.table.isDismissed(task.domain, e.now()) {
				e.obs.AckOpen(observation.ConnectionID)
				continue
			}
			_, allowed, retry := e.coverageAllowsDomainStatus(ctx, observation.Host, task.domain, false)
			if allowed && e.enqueue(task) {
				e.obs.AckOpen(observation.ConnectionID)
				log.Printf("[autolist] %s: открытое подозрение, добавлено в очередь пробы", task.host)
			} else if !allowed && !retry {
				e.obs.AckOpen(observation.ConnectionID)
			}
			continue
		}
		if e.noteIfUnrouted(ctx, observation.Host, observation.Why, source) {
			e.enqueue(task)
		}
	}
	for _, observation := range e.obs.PollLogs(ctx) {
		e.debugf("журнал: host=%s ip=%s why=%s", observation.Host.Name, observation.Host.IP, observation.Why)
		if e.noteIfUnrouted(ctx, observation.Host, observation.Why, "dial") {
			e.enqueue(probeTask{
				host: observation.Host.Name, domain: registeredDomain(observation.Host.Name), ip: observation.Host.IP,
				why: observation.Why, source: "dial",
			})
		}
	}
}

// packetCorrelationWorker не делит goroutine с основным tick: серия дорогих
// coverage/inspect проверок там не должна откладывать короткие RST-потоки.
func (e *Engine) packetCorrelationWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.packetWake:
			e.obs.PollRoutes(ctx)
			e.debugf("внеочередной снимок Clash для packet correlation завершён")
			e.flushPendingPackets(ctx)
		case result := <-e.dispatch:
			// Весь coverage/inspect HTTP находится вне judge-owner.
			e.dispatchPacketResult(ctx, result)
		}
	}
}

// wakePacketCorrelation коалесцирует SYN/HELLO в частые, но ограниченные
// внеочередные снимки Clash. Callback выполняется в judge-owner, поэтому HTTP
// здесь не вызывается и пакетный цикл не блокируется. Короткий интервал нужен,
// чтобы повтор на HELLO не был поглощён более ранним SYN до публикации потока.
func (e *Engine) wakePacketCorrelation() {
	now := e.now()
	e.packetMu.Lock()
	if !e.packetPollAt.IsZero() && now.Sub(e.packetPollAt) < 50*time.Millisecond {
		e.packetMu.Unlock()
		return
	}
	e.packetPollAt = now
	e.packetMu.Unlock()
	select {
	case e.packetWake <- struct{}{}:
	default:
	}
}

// processPacketResult вызывается только judge-owner'ом packetruntime. До
// решения Clash результат ожидает короткий bounded TTL; unknown никогда не
// превращается в кандидата или активную пробу.
func (e *Engine) processPacketResult(_ context.Context, result packetbridge.Result) {
	if failure := result.Failure; failure != nil {
		e.debugf("packet failure kind=%s host=%q listed=%v bypass=%v",
			result.Kind, failure.Host, failure.Listed, failure.Bypass)
	}
	if !e.Config().PacketEnabled || result.Bypass != nil ||
		(result.Failure != nil && result.Failure.Bypass) ||
		(result.Nomination != nil && result.Nomination.Bypass) {
		return
	}
	if result.Failure != nil && !result.Failure.Listed {
		return
	}
	if result.Failure == nil && result.Nomination == nil {
		return
	}

	now := e.now()
	route := e.obs.routes.Classify(result.Flow, now)
	e.debugf("packet result kind=%s flow=%s:%d -> %s:%d route=%s",
		result.Kind, result.Flow.Src, result.Flow.SrcPort, result.Flow.Dst, result.Flow.DstPort, route)
	switch route {
	case directtrack.Direct:
		select {
		case e.dispatch <- result:
		default:
			e.packetMu.Lock()
			e.dispatchDrop++
			e.packetMu.Unlock()
		}
	case directtrack.NonDirect:
		e.packetMu.Lock()
		e.nonDirect++
		e.packetMu.Unlock()
	default:
		// Даже если поток уже закрывается, повторный снимок может застать его
		// запись либо завершить ожидание по более раннему SYN/HELLO снимку.
		e.wakePacketCorrelation()
		e.packetMu.Lock()
		defer e.packetMu.Unlock()
		if old, exists := e.pending[result.Flow]; exists {
			old.result = result
			e.pending[result.Flow] = old // сохраняем исходный TTL
			return
		}
		if len(e.pending) >= PacketPendingCapacity {
			var oldest packet.Flow
			var oldestAt time.Time
			first := true
			for flow, item := range e.pending {
				if first || item.at.Before(oldestAt) {
					oldest, oldestAt, first = flow, item.at, false
				}
			}
			if !first {
				delete(e.pending, oldest)
			}
			e.pendingDrop++
		}
		e.pending[result.Flow] = pendingPacket{result: result, at: now}
	}
}

func (e *Engine) flushPendingPackets(ctx context.Context) {
	now := e.now()
	var ready []packetbridge.Result
	e.packetMu.Lock()
	for flow, item := range e.pending {
		if !now.Before(item.at) && now.Sub(item.at) > PacketCorrelationTTL {
			delete(e.pending, flow)
			e.pendingTTL++
			continue
		}
		switch e.obs.routes.Classify(flow, now) {
		case directtrack.Direct:
			ready = append(ready, item.result)
			delete(e.pending, flow)
		case directtrack.NonDirect:
			delete(e.pending, flow)
			e.nonDirect++
		}
	}
	e.packetMu.Unlock()
	for _, result := range ready {
		e.dispatchPacketResult(ctx, result)
	}
}

func (e *Engine) dispatchPacketResult(ctx context.Context, result packetbridge.Result) {
	if !e.Config().PacketEnabled || ctx.Err() != nil {
		return
	}
	ip := result.Flow.Dst.String()
	if failure := result.Failure; failure != nil {
		if !failure.Listed || failure.Bypass {
			return
		}
		domain := failure.Domain
		if domain == "" {
			domain = registeredDomain(failure.Host)
		}
		h := Host{Name: failure.Host, IP: ip}
		if _, ok := e.coverageAllowsDomain(ctx, h, domain, true); !ok {
			return
		}
		why := WhyNoAnswer
		if failure.Why == judge.WhyReset {
			why = WhyReset
		}
		// Listed HELLO/RESET создают обычного кандидата: auto-add разрешён
		// только после подтверждения BLOCKED активной пробой в completeProbe.
		if e.table.noteDomain(h, domain, why, "packet", e.now()) {
			e.enqueue(probeTask{
				host: failure.Host, domain: domain, ip: ip, why: why, source: "packet",
				byDomain: failure.Domain != "",
			})
		}
		return
	}

	nomination := result.Nomination
	if nomination == nil || nomination.Bypass {
		return
	}
	domain := nomination.Domain
	if domain == "" {
		domain = registeredDomain(nomination.Host)
	}
	h := Host{Name: nomination.Host, IP: ip}
	if _, ok := e.coverageAllowsDomain(ctx, h, domain, true); !ok {
		return
	}
	why := WhyNoConnect
	if nomination.Kind == judge.NominationStall {
		why = WhyFreeze
	} else if nomination.Kind != judge.NominationNoConnect {
		return
	}
	if !e.table.noteDomain(h, domain, why, "packet", e.now()) {
		return
	}
	// Stall/SYN сохраняют обычный candidate -> CHECKING -> probe lifecycle.
	e.enqueue(probeTask{
		host: nomination.Host, domain: domain, ip: ip, why: why, source: "packet",
		byDomain: nomination.Domain != "",
	})
}

// enqueue добавляет низкоприоритетную номинацию. Первый активный запрос нового
// кандидата будит worker с разрешением NOTE; probe-only наблюдение по-прежнему
// ждёт обычный 120-секундный тик. Queue coalescing подавляет повторные пробы.
func (e *Engine) enqueue(task probeTask) bool {
	if task.host == "" || task.domain == "" {
		return false
	}
	if task.probeOnly {
		if _, exists := e.table.get(task.domain); exists {
			task.probeOnly = false
		}
	}

	e.mu.Lock()
	// Решение фиксируется при постановке, а не при извлечении из очереди:
	// dismiss/config change между ними обязаны сделать задачу устаревшей.
	task.domainRevision = e.domainRevision[task.domain]
	task.configRevision = e.configRevision
	task.targetInterface = e.cfg.TargetInterface
	byDomain := task.byDomain || task.host == task.domain
	ask := e.probeQueue.Nominate(task.host, byDomain, e.now().Unix())
	if ask != probeq.AskQueued {
		e.mu.Unlock()
		return false
	}
	// Nominate также возвращает AskQueued для live in-flight хоста, но в этом
	// случае новой записи в очереди нет и метаданные заменять не нужно.
	if e.probingHost != task.host {
		e.probeTasks[task.host] = task
	}
	e.mu.Unlock()
	if !task.probeOnly {
		// kickProbe сам берёт e.mu: вызывать его можно только после Unlock.
		e.kickProbe(true)
	}
	return true
}

// RequestProbe ставит ручной USER-запрос перед recheck и обычными номинациями.
// В отличие от Nominate он обходит result cache, но probeq сохраняет
// необязательные host spacing/часовой бюджет и защиту от live in-flight дублей.
func (e *Engine) RequestProbe(host string) probeq.Ask {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	task := probeTask{
		host: host, domain: registeredDomain(host), source: "user", probeOnly: true,
	}
	if _, exists := e.table.get(task.domain); exists {
		task.probeOnly = false
	}

	e.mu.Lock()
	task.domainRevision = e.domainRevision[task.domain]
	task.configRevision = e.configRevision
	task.targetInterface = e.cfg.TargetInterface
	ask := e.probeQueue.Request(host, e.now().Unix())
	if ask == probeq.AskQueued {
		// При upgrade NOTE/RECHECK сохраняем наблюдаемый IP и причину: ручной
		// запрос меняет приоритет, а не происхождение активной пробы.
		if old, ok := e.probeTasks[host]; ok {
			task.ip = old.ip
			task.why = old.why
		}
		e.probeTasks[host] = task
	}
	e.mu.Unlock()
	if ask == probeq.AskQueued {
		e.kickProbe(false)
	}
	return ask
}

// nextProbe выдаёт не больше одной задачи единственному worker. Без аргумента
// сохраняется прежнее поведение тестового helper'а: номинации разрешены.
func (e *Engine) nextProbe(allowNotes ...bool) (probeTask, bool) {
	notes := true
	if len(allowNotes) > 0 {
		notes = allowNotes[0]
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.workerBusy {
		return probeTask{}, false
	}
	queued, ok := e.probeQueue.Next(e.now().Unix(), notes)
	if !ok {
		return probeTask{}, false
	}
	task, ok := e.probeTasks[queued.Host]
	if ok {
		delete(e.probeTasks, queued.Host)
	} else {
		// У probeq нет autolist-метаданных. Fallback нужен для прямых вызовов
		// Queue и не теряет основной инвариант: host и domain разные ключи.
		task = probeTask{host: queued.Host, domain: registeredDomain(queued.Host), probeOnly: true}
		if _, exists := e.table.get(task.domain); exists {
			task.probeOnly = false
		}
	}
	task.byDomain = queued.ByDomain
	task.probeInterface = e.cfg.ProbeInterface
	task.wanURL = strings.TrimSpace(e.cfg.WANCheckURL)
	if task.wanURL == "" {
		task.wanURL = DefaultWANCheckURL
	}
	e.workerBusy = true
	e.probingHost = queued.Host
	return task, true
}

// kickProbe коалесцирует будильники и отдельно запоминает разрешение NOTE:
// новые кандидаты и 120-с tick разрешают NOTE, а USER/RECHECK их не выкачивают.
func (e *Engine) kickProbe(allowNotes bool) {
	if allowNotes {
		e.mu.Lock()
		e.probeNotesDue = true
		e.mu.Unlock()
	}
	select {
	case e.probeWake <- struct{}{}:
	default:
	}
}

func (e *Engine) probeWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.probeWake:
		}

		e.mu.Lock()
		allowNotes := e.probeNotesDue
		e.probeNotesDue = false
		e.mu.Unlock()
		if !e.runNextProbe(ctx, allowNotes) {
			continue
		}
		// USER/RECHECK (allowNotes=false) по-прежнему не выкачивают NOTE.
		// Candidate/tick wake может слить coalesced NOTE backlog; Next сохраняет
		// необязательный часовой лимит, а workerBusy — единственную активную пробу.
		for ctx.Err() == nil && e.runNextProbe(ctx, allowNotes) {
		}
	}
}

func (e *Engine) runNextProbe(ctx context.Context, allowNotes bool) bool {
	if ctx.Err() != nil {
		return false
	}
	task, ok := e.nextProbe(allowNotes)
	if !ok {
		return false
	}
	e.probeOneContext(ctx, task)
	return true
}

func (e *Engine) probeOne(task probeTask) {
	e.probeOneContext(context.Background(), task)
}

func (e *Engine) probeTunnel(ctx context.Context, task probeTask) (Verdict, string) {
	if task.targetInterface == "" {
		return VerdictUnknown, "не выбран интерфейс для туннельной пробы"
	}
	// Тот же Prober и те же HTTP/body/watchdog-правила, что у direct. Меняется
	// только kernel-интерфейс; observed IP намеренно не фиксируется.
	prober := *e.prober
	if e.tunnelProber != nil {
		prober = *e.tunnelProber
	}
	prober.Interface = task.targetInterface
	prober.WANURL = task.wanURL
	prober.WANExpects204 = true
	res := prober.Probe(ctx, task.host)
	return res.Verdict, res.Err
}

func (e *Engine) probeOneContext(parent context.Context, task probeTask) {
	taskCtx, cancelTask := context.WithCancel(parent)
	defer cancelTask()
	e.mu.Lock()
	e.taskCancels[task.host] = cancelTask
	e.activeDomain = task.domain
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.taskCancels, task.host)
		e.activeDomain = ""
		e.mu.Unlock()
		e.releaseDomainRevision(task.domain)
	}()
	task.ctx = taskCtx
	cfg := e.Config()
	probeBudget := ProbeTimeoutSec + ProbeStallSec + 2
	timeoutSec := probeBudget
	if cfg.TunnelProbe {
		timeoutSec = 2 * probeBudget
	}
	ctx, cancel := context.WithTimeout(taskCtx, time.Duration(timeoutSec)*time.Second)
	defer cancel()
	if !task.probeOnly {
		e.table.MarkChecking(task.domain)
	}
	where := task.host
	if task.ip != "" {
		where += " (" + task.ip + ")"
	}
	log.Printf("[autolist] %s: активная проба началась", where)
	prober := *e.prober
	prober.Interface = task.probeInterface
	prober.WANURL = task.wanURL
	prober.WANExpects204 = true
	res := prober.ProbeAt(ctx, task.host, task.ip)
	if res.Verdict == VerdictBlocked && cfg.TunnelProbe {
		if !task.probeOnly {
			e.table.MarkTunnelChecking(task.domain)
		}
		res.TunnelProbed = true
		res.TunnelVerdict, res.TunnelErr = e.probeTunnel(ctx, task)
		e.debugf("туннельная проба: host=%s verdict=%s err=%q", task.host, res.TunnelVerdict, res.TunnelErr)
	}
	e.completeProbe(task, res, e.now())
}

// completeProbe завершает одну задачу без чтения или записи verdict cache.
// Generation и target сверяются снова непосредственно перед записью правила.
func (e *Engine) completeProbe(task probeTask, res ProbeResult, completed time.Time) {
	e.mu.Lock()
	workerTask := e.workerBusy && e.probingHost == task.host
	e.probeQueue.Done(task.host, completed.Unix())
	delete(e.probeTasks, task.host)
	if e.probingHost == task.host {
		e.probingHost = ""
	}
	current := e.domainRevision[task.domain] == task.domainRevision &&
		e.configRevision == task.configRevision && e.cfg.TargetInterface == task.targetInterface
	auto := e.cfg.AutoAdd && e.cfg.TunnelProbe
	e.mu.Unlock()
	defer func() {
		if workerTask {
			e.mu.Lock()
			e.workerBusy = false
			e.mu.Unlock()
		}
	}()
	e.debugf("проба завершена: host=%s verdict=%s why=%s bytes=%d err=%q", task.host, res.Verdict, res.Why, res.Bytes, res.Err)
	if !current {
		e.table.ResetChecking(task.domain)
		log.Printf("[autolist] %s: результат отброшен: задача или целевой интерфейс изменились", task.host)
		return
	}

	switch res.Verdict {
	case VerdictBlocked:
		log.Printf("[autolist] %s BLOCKED directly (%s): %s", task.host, res.Why, res.Err)
		allowAuto := task.domain != ""
		if task.probeOnly {
			why := task.why
			if why == WhyNone {
				why = res.Why
			}
			source := task.source
			if source == "" {
				source = "probe"
			}
			allowAuto = e.table.Note(Host{Name: task.host, IP: task.ip}, why, source, completed)
		}
		if auto && allowAuto {
			if !res.TunnelProbed || res.TunnelVerdict != VerdictReachable {
				log.Printf("[autolist] %s: автодобавление пропущено, туннельная проба=%s", task.host, res.TunnelVerdict)
			} else if err := e.addTaskToTunnel(task); err != nil {
				log.Printf("[autolist] %s: автодобавление не выполнено: %v", task.host, err)
			}
		}
	case VerdictReachable:
		// Успех завершает только текущее событие: никакого AutoOff, суточного
		// dismiss или записи в историю, которая подавляла бы следующее событие.
		log.Printf("[autolist] %s reachable directly", task.host)
	default:
		log.Printf("[autolist] %s unknown: %s", task.host, res.Err)
	}
	e.table.Confirm(task.domain, res.Verdict, completed)
	if res.TunnelProbed {
		e.table.ConfirmTunnel(task.domain, res.TunnelVerdict, completed)
	}
	e.table.SetProbeDiagnostics(task.domain, res.Err, res.TunnelErr)
}

// addToTunnel добавляет домены отдельными правилами domain_suffix → outbound
// (каждый домен — своё правило, как просил пользователь). Общий потолок
// AddsPerHour в час действует и на ручные, и на автоматические добавления.
// Возвращает карту домен → ошибка (пустая — успех).
func (e *Engine) addToTunnel(domains []string) map[string]error {
	return e.addToTunnelContext(context.Background(), domains)
}

func (e *Engine) addToTunnelContext(parent context.Context, domains []string) map[string]error {
	cfg := e.Config()
	return e.addToTunnelChecked(parent, domains, 0, 0, cfg.TargetInterface, false)
}

func (e *Engine) addTaskToTunnel(task probeTask) error {
	parent := task.ctx
	if parent == nil {
		parent = context.Background()
	}
	res := e.addToTunnelChecked(parent, []string{task.domain}, task.domainRevision, task.configRevision, task.targetInterface, true)
	return res[task.domain]
}

func (e *Engine) addToTunnelChecked(parent context.Context, domains []string, domainRevision, configRevision uint64, targetInterface string, automatic bool) map[string]error {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	res := map[string]error{}
	if len(domains) == 0 {
		return res
	}
	if err := parent.Err(); err != nil {
		for _, d := range domains {
			res[d] = err
		}
		return res
	}
	if targetInterface == "" {
		for _, d := range domains {
			res[d] = errors.New("не выбран интерфейс для автосписка")
		}
		return res
	}
	e.mu.Lock()
	stale := automatic && (e.domainRevision[domains[0]] != domainRevision || e.configRevision != configRevision || e.cfg.TargetInterface != targetInterface || !e.cfg.AutoAdd || !e.cfg.TunnelProbe)
	e.mu.Unlock()
	if stale {
		for _, d := range domains {
			res[d] = errors.New("решение устарело или автодобавление выключено")
		}
		return res
	}
	now := e.now()
	// Короткая блокировка только для окна добавлений, не для router HTTP.
	e.addsMu.Lock()
	fresh := e.addsWindow[:0]
	for _, t := range e.addsWindow {
		if now.Sub(t) < time.Hour {
			fresh = append(fresh, t)
		}
	}
	e.addsWindow = fresh
	budget := AddsPerHour - len(e.addsWindow)
	e.addsMu.Unlock()
	if budget <= 0 {
		log.Printf("[autolist] %d добавлений за час — лимит; новые домены ждут (похоже на аварию сети, а не блокировки)", AddsPerHour)
	}

	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()

	// Повторное добавление уже управляемого домена — успешный no-op, а не
	// ошибка «уже покрыт». Это также не расходует часовой лимит добавлений.
	existingList, err := e.rules.ListDomains(ctx)
	if err != nil {
		for _, d := range domains {
			res[d] = err
		}
		return res
	}
	existing := map[string]bool{}
	for _, d := range existingList {
		existing[d] = true
	}

	// Проверка покрытия по каждому новому домену (домен + резолв).
	var accepted []string
	for _, domain := range domains {
		if existing[domain] {
			e.table.Forget(domain)
			continue
		}
		if len(accepted) >= budget {
			res[domain] = errors.New("лимит добавлений в час исчерпан")
			continue
		}
		if err := e.checkCoverage(ctx, domain); err != nil {
			res[domain] = err
			continue
		}
		accepted = append(accepted, domain)
	}

	// Один вызов на все домены: единый набор, одно горячее обновление live SRS.
	if len(accepted) > 0 {
		if automatic {
			e.mu.Lock()
			stale = e.domainRevision[domains[0]] != domainRevision || e.configRevision != configRevision || e.cfg.TargetInterface != targetInterface || !e.cfg.AutoAdd || !e.cfg.TunnelProbe
			e.mu.Unlock()
			if stale {
				for _, d := range accepted {
					res[d] = errors.New("решение устарело или автодобавление выключено")
				}
				return res
			}
		}
		if err := e.rules.AddDomainSuffixRule(ctx, accepted, targetInterface); err != nil {
			for _, d := range accepted {
				res[d] = err
			}
			return res
		}
		e.addsMu.Lock()
		for range accepted {
			e.addsWindow = append(e.addsWindow, now)
		}
		e.addsMu.Unlock()
		for _, d := range accepted {
			delete(res, d) // успех — ошибки нет
			e.table.Forget(d)
			log.Printf("[autolist] %s: добавлен в набор %s -> interface %s", d, ruleSetTag, targetInterface)
		}
	}
	return res
}

// checkCoverage — домен и его резолв не должны быть уже покрыты
// маршрутизацией (list/geosite/ip_cidr/geoip).
func (e *Engine) checkCoverage(ctx context.Context, domain string) error {
	excluded, err := e.excluded(ctx, domain, domain)
	if err != nil {
		return fmt.Errorf("исключения не проверены: %w", err)
	}
	if excluded {
		e.table.Forget(domain)
		return errors.New("домен входит в исключения")
	}
	ins, err := e.rules.Inspect(ctx, domain)
	if err != nil {
		return fmt.Errorf("правила не проверены: %w", err)
	}
	if ins.HasRoutingDecision() {
		e.table.Forget(domain)
		return fmt.Errorf("домен уже покрыт правилами роутера")
	}
	e.mu.Lock()
	resolveVia := e.cfg.ResolveVia
	resolveIP := e.cfg.ResolveIPOn()
	e.mu.Unlock()
	if resolveIP {
		ips, err := e.rules.ResolveDomain(ctx, domain, resolveVia)
		if err != nil {
			return fmt.Errorf("резолв для IP-покрытия не выполнен: %w", err)
		}
		for _, ip := range ips {
			ipIns, err := e.rules.Inspect(ctx, ip)
			if err != nil {
				return fmt.Errorf("IP %s не проверен: %w", ip, err)
			}
			if ipIns.HasRoutingDecision() {
				e.table.Forget(domain)
				return fmt.Errorf("IP домена уже покрыт ip_cidr/geoip")
			}
		}
	}
	return nil
}

// AddToTunnel — публичное ручное массовое добавление для HTTP /add.
func (e *Engine) AddToTunnel(domains []string) map[string]error {
	return e.addToTunnelContext(context.Background(), domains)
}

// Snapshot — JSON-состояние для /status сателлита.
func (e *Engine) Snapshot() map[string]any {
	e.mu.Lock()
	cfg := e.cfg
	inFlight := 0
	if e.probingHost != "" {
		inFlight = 1
	}
	e.mu.Unlock()
	usedHour, queued := e.probeQueue.Counts(e.now().Unix())
	now := e.now()
	addsUsedHour := 0
	e.addsMu.Lock()
	for _, at := range e.addsWindow {
		if now.Sub(at) < time.Hour {
			addsUsedHour++
		}
	}
	e.addsMu.Unlock()
	packetStatus := e.packet.Snapshot()
	e.packetMu.Lock()
	pending := len(e.pending)
	pendingDropped, pendingExpired, nonDirect := e.pendingDrop, e.pendingTTL, e.nonDirect
	dispatchDropped := e.dispatchDrop
	e.packetMu.Unlock()
	return map[string]any{
		"candidates":      e.table.List(),
		"history":         e.history.list(),
		"autoAdd":         cfg.AutoAdd,
		"targetInterface": cfg.TargetInterface,
		"probesQueued":    queued,
		"probesInFlight":  inFlight,
		"probesUsedHour":  usedHour,
		"probesLimit":     probeq.StartsPerHour,
		"addsUsedHour":    addsUsedHour,
		"addsLimit":       AddsPerHour,

		"packetEnabled":            cfg.PacketEnabled,
		"packetBackend":            packetStatus.Backend,
		"packetRunning":            packetStatus.Running,
		"packetConntrackRunning":   packetStatus.ConntrackRunning,
		"packetConntrackFlows":     packetStatus.ConntrackFlows,
		"packetConntrackErrors":    packetStatus.ConntrackErrors,
		"packetLastError":          packetStatus.LastError,
		"packetEvents":             packetStatus.Events,
		"packetMalformed":          packetStatus.Malformed,
		"packetLost":               packetStatus.Lost,
		"packetReadErrors":         packetStatus.ReadErrors,
		"packetQueueDepth":         packetStatus.QueueDepth,
		"packetQueueDropped":       packetStatus.QueueDropped,
		"packetDNSAnswers":         packetStatus.DNSAnswers,
		"packetDNSErrors":          packetStatus.DNSErrors,
		"packetPending":            pending,
		"packetPendingDropped":     pendingDropped,
		"packetResultDropped":      dispatchDropped,
		"packetCorrelationExpired": pendingExpired,
		"packetNonDirect":          nonDirect,
	}
}
