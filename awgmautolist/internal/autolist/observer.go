package autolist

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/awgmautolist/internal/directtrack"
	"github.com/hoaxisr/awg-manager/awgmautolist/internal/packet"
)

// Observation — номинация хоста одной из стратегий autolist.
type Observation struct {
	Host Host
	Why  Why
	// ConnectionID позволяет Engine подтвердить принятие open-события только
	// после постановки задачи в очередь.
	ConnectionID string
	// ProbeOnly — подозрение по открытому соединению. Такое наблюдение не
	// является autolist-номинацией и до VerdictBlocked не создаёт кандидата.
	ProbeOnly bool
}

// Observer собирает «подозреваемых» из двух источников awg-manager:
//  1. поток соединений Clash API (GET /api/singbox/clash/connections) —
//     FIN-стратегия autolist: соединение закрылось после 8..32 КБ ответа;
//  2. журнал sing-box (GET /api/logs?bucket=singbox) — таймауты direct dial,
//     аналог стратегии autolist для SYN без ответа.
//
// Clash API не отдаёт SYN/ClientHello/RST-пакеты. Медленный рост открытого
// соединения никогда не заменяет FIN-доказательство: он лишь запускает
// отдельную активную пробу (ProbeOnly), которая обязана подтвердить блокировку.
type Observer struct {
	base    string // http://router:2222
	apiKey  string
	http    *http.Client
	now     func() time.Time
	instant func() bool

	mu        sync.Mutex
	stallMu   sync.Mutex
	noConnect map[string]*tally
	stall     map[string]*tally
	stallPrev map[string]stallPrev            // ключ соединения -> прошлый срез
	hints     map[string]map[string]time.Time // serverIP -> не истёкшие имена
	lastLogTS string

	// routes хранит и direct, и non-direct пятитаплы. AF_PACKET видит трафик
	// до решения sing-box, поэтому отсутствие записи — unknown, а не разрешение
	// считать соединение прямым.
	routes *directtrack.Tracker
}

// stallPrev — снимок соединения, необходимый для FIN-стратегии autolist.
// Пока соединение открыто, никаких отказов оно не даёт.
const (
	maxHintNames         = 16
	openEventMaxAttempts = 3
)

type stallPrev struct {
	download     int64
	startAt      time.Time
	at           time.Time // прошлый срез для probe-only оценки скорости
	host         Host
	probed       bool // open-событие принято в очередь или окончательно отклонено
	openAttempts int
	openRetryAt  time.Time
}

// tally — счётчик autolist: окно продолжается от последнего события, а после
// срабатывания начинает заново. Поэтому номинация выдаётся один раз на порог.
type tally struct {
	n    int
	last time.Time
}

func (o *Observer) instantEnabled() bool {
	return o.instant != nil && o.instant()
}

func (o *Observer) failureThreshold() int {
	if o.instantEnabled() {
		return 1
	}
	return DefaultFailThreshold
}

func (o *Observer) stallThreshold() int {
	if o.instantEnabled() {
		return 1
	}
	return StallNotesThreshold
}

func NewObserver(base, apiKey string) *Observer {
	return &Observer{
		base:      strings.TrimRight(base, "/"),
		apiKey:    apiKey,
		http:      &http.Client{Timeout: PollTimeoutSec * time.Second},
		now:       time.Now,
		noConnect: map[string]*tally{},
		stall:     map[string]*tally{},
		stallPrev: map[string]stallPrev{},
		hints:     map[string]map[string]time.Time{},
		routes:    directtrack.New(0, 0),
	}
}

// clashEnvelope — формат ответа Clash API (см. internal/singbox/clash.go).
type clashEnvelope struct {
	Connections []clashConn `json:"connections"`
}

type clashConn struct {
	ID       string `json:"id"`
	Metadata struct {
		Network         string `json:"network"`
		SourceIP        string `json:"sourceIP"`
		SourcePort      string `json:"sourcePort"`
		DestinationIP   string `json:"destinationIP"`
		DestinationPort string `json:"destinationPort"`
		Host            string `json:"host"`
	} `json:"metadata"`
	Upload   int64    `json:"upload"`
	Download int64    `json:"download"`
	Start    string   `json:"start"`
	Outbound string   `json:"outbound"`
	Chains   []string `json:"chains"`
}

// carryingOutbound — соединение уже идёт через туннель/прокси: его маршрут
// решён, «неоткрывающимся» оно быть не может. Заморозка интересует только
// на direct-соединениях.
func carryingOutbound(outbound string, chains []string) bool {
	if outbound != "" && outbound != "direct" {
		return true
	}
	for _, c := range chains {
		if c != "" && c != "direct" {
			return true
		}
	}
	return false
}

func clashFlow(sourceIP, sourcePort, destinationIP, destinationPort string) (packet.Flow, bool) {
	src, err := netip.ParseAddr(sourceIP)
	if err != nil {
		return packet.Flow{}, false
	}
	dst, err := netip.ParseAddr(destinationIP)
	if err != nil {
		return packet.Flow{}, false
	}
	srcPort, err := strconv.ParseUint(sourcePort, 10, 16)
	if err != nil || srcPort == 0 {
		return packet.Flow{}, false
	}
	dstPort, err := strconv.ParseUint(destinationPort, 10, 16)
	if err != nil || dstPort == 0 {
		return packet.Flow{}, false
	}
	return packet.Flow{
		Src: src, Dst: dst, SrcPort: uint16(srcPort), DstPort: uint16(dstPort),
		Protocol: packet.ProtocolTCP,
	}, true
}

// observationKey объединяет события разных хостов одного регистрируемого
// домена, как domain_tally в autolist judge.c. Без домена событие всё равно будет
// отклонено Engine; ключ по имени здесь лишь безопасный fallback.
func observationKey(h Host) string {
	if domain := registeredDomain(h.Name); domain != "" {
		return domain
	}
	return h.key()
}

func (o *Observer) fetchConnections(ctx context.Context) ([]clashConn, bool) {
	body, err := o.get(ctx, "/api/singbox/clash/connections")
	if err != nil {
		return nil, false
	}
	var env clashEnvelope
	if json.Unmarshal(body, &env) != nil {
		return nil, false
	}
	return env.Connections, true
}

// observeRoute сохраняет только решение маршрута. Tracker сам сериализует
// доступ, поэтому этот узкий путь безопасно использует packet-worker параллельно
// с основным FIN/stall-наблюдением.
func (o *Observer) observeRoute(c clashConn, now time.Time) {
	m := c.Metadata
	if m.Network != "" && m.Network != "tcp" {
		return
	}
	if m.DestinationPort != "443" {
		return
	}
	if flow, ok := clashFlow(m.SourceIP, m.SourcePort, m.DestinationIP, m.DestinationPort); ok {
		o.routes.Observe(flow, !carryingOutbound(c.Outbound, c.Chains), now)
	}
}

// PollRoutes обновляет только direct/non-direct атрибуцию для packet runtime.
// Он не читает и не изменяет tally, stallPrev или закрытия соединений.
func (o *Observer) PollRoutes(ctx context.Context) {
	connections, ok := o.fetchConnections(ctx)
	if !ok {
		return
	}
	now := o.now()
	for _, c := range connections {
		o.observeRoute(c, now)
	}
}

// AckOpen фиксирует terminal-решение по open-событию: задача принята в очередь
// либо домен окончательно исключён/уже маршрутизируется. Временная ошибка
// подготовки и переполненная очередь AckOpen не вызывают.
func (o *Observer) AckOpen(connectionID string) {
	if connectionID == "" {
		return
	}
	o.stallMu.Lock()
	defer o.stallMu.Unlock()
	if prev, ok := o.stallPrev[connectionID]; ok {
		prev.probed = true
		o.stallPrev[connectionID] = prev
	}
}

// releaseOpenHost разрешает ограниченный повтор событий, чья задача была
// вытеснена до начала пробы. Dismiss/exclude используют Cancel, а не eviction,
// поэтому это разблокирование к ним не относится.
func (o *Observer) releaseOpenHost(host string) {
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	o.stallMu.Lock()
	defer o.stallMu.Unlock()
	for id, prev := range o.stallPrev {
		if prev.host.Name != host || !prev.probed || prev.openAttempts >= openEventMaxAttempts {
			continue
		}
		prev.probed = false
		prev.openRetryAt = time.Time{}
		o.stallPrev[id] = prev
	}
}

// PollConnections переносит autolist_fin на доступные срезы Clash API:
// исчезновение соединения из среза = его закрытие. Условия строго как в autolist:
//   - TCP/443 direct;
//   - соединение прожило не меньше 10 секунд;
//   - всего получено от 8192 до 32768 байт включительно;
//   - два таких закрытия домена за 120 секунд.
//
// Открытое, неподвижное или медленно растущее соединение НЕ является
// FIN-кандидатом. Отдельный AWGM probe-only сигнал может отправить его на
// активную проверку, но кандидат и правило появляются только после blocked.
// Обычный keepalive и короткий полный ответ поэтому не попадают в таблицу.
func (o *Observer) PollConnections(ctx context.Context) []Observation {
	connections, ok := o.fetchConnections(ctx)
	if !ok {
		return nil
	}
	o.stallMu.Lock()
	defer o.stallMu.Unlock()
	now := o.now()
	var out []Observation
	seen := map[string]bool{}
	for _, c := range connections {
		o.observeRoute(c, now)
		m := c.Metadata
		if m.Network != "" && m.Network != "tcp" {
			continue
		}
		if m.DestinationPort != "443" {
			continue
		}
		if m.Host != "" && m.DestinationIP != "" && !privateIP(m.DestinationIP) {
			o.addHint(m.DestinationIP, m.Host, now)
		}
		if carryingOutbound(c.Outbound, c.Chains) {
			continue
		}
		h := Host{IP: m.DestinationIP, Name: strings.ToLower(strings.TrimSuffix(m.Host, "."))}
		if h.IP != "" && privateIP(h.IP) {
			continue
		}
		seen[c.ID] = true

		// Больше 32 КБ — вне полосы FIN-стратегии. Если соединение уже
		// отслеживалось, снимаем его, чтобы закрытие не использовало старый
		// срез с меньшим download.
		if c.Download > StallMaxBytes {
			delete(o.stallPrev, c.ID)
			continue
		}
		prev, tracked := o.stallPrev[c.ID]
		if !tracked {
			if len(o.stallPrev) >= FlowsCap {
				oldestID := ""
				var oldestAt time.Time
				for id, item := range o.stallPrev {
					if oldestID == "" || item.at.Before(oldestAt) {
						oldestID, oldestAt = id, item.at
					}
				}
				if oldestID != "" {
					delete(o.stallPrev, oldestID)
				}
			}
			startAt := now
			if t, err := time.Parse(time.RFC3339, c.Start); err == nil {
				startAt = t
			}
			o.stallPrev[c.ID] = stallPrev{download: c.Download, startAt: startAt, at: now, host: h}
			continue
		}
		// AWGM-расширение для открытого tarpit: низкая скорость после 8 КБ
		// и возраст от 30 с дают ТОЛЬКО активную пробу. FIN-счётчик не
		// изменяется, а кандидат появится лишь после VerdictBlocked.
		elapsed := now.Sub(prev.at)
		gained := c.Download - prev.download
		if !prev.probed && prev.openAttempts < openEventMaxAttempts && !now.Before(prev.openRetryAt) &&
			elapsed > 0 && now.Sub(prev.startAt) >= OpenProbeMinSecs*time.Second &&
			c.Download >= StallMinUplink && gained >= 0 &&
			float64(gained)/elapsed.Seconds() < OpenProbeMaxBPS && registeredDomain(h.Name) != "" {
			out = append(out, Observation{Host: h, Why: WhyFreeze, ProbeOnly: true, ConnectionID: c.ID})
			prev.openAttempts++
			prev.openRetryAt = now.Add(PollIntervalSec * time.Second)
		}
		prev.download = c.Download
		prev.at = now
		if prev.host.Name == "" {
			prev.host.Name = h.Name
		}
		if prev.host.IP == "" {
			prev.host.IP = h.IP
		}
		o.stallPrev[c.ID] = prev
	}

	// Закрытия: были в прошлом срезе, исчезли сейчас.
	for id, prev := range o.stallPrev {
		if seen[id] {
			continue
		}
		delete(o.stallPrev, id)
		if now.Sub(prev.startAt) < StallMinSecs*time.Second {
			continue
		}
		if prev.download < StallMinUplink || prev.download > StallMaxBytes {
			continue
		}
		if registeredDomain(prev.host.Name) == "" {
			continue
		}
		if bump(o.stall, observationKey(prev.host), o.stallThreshold(), StallNoteWindow*time.Second, now) {
			out = append(out, Observation{Host: prev.host, Why: WhyFreeze})
		}
	}
	return out
}

// addHint запоминает все имена адреса. Одного последнего SNI недостаточно:
// на общем CDN он мог принадлежать другому домену.
func (o *Observer) addHint(ip, name string, now time.Time) {
	name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	if ip == "" || name == "" {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	names := o.hints[ip]
	if names == nil {
		if len(o.hints) >= HostsCap {
			oldestIP := ""
			var oldestAt time.Time
			for candidate, entries := range o.hints {
				for _, at := range entries {
					if oldestIP == "" || at.Before(oldestAt) {
						oldestIP, oldestAt = candidate, at
					}
				}
			}
			if oldestIP != "" {
				delete(o.hints, oldestIP)
			}
		}
		names = map[string]time.Time{}
		o.hints[ip] = names
	}
	if _, exists := names[name]; !exists && len(names) >= maxHintNames {
		oldestName := ""
		var oldestAt time.Time
		for candidate, at := range names {
			if oldestName == "" || at.Before(oldestAt) {
				oldestName, oldestAt = candidate, at
			}
		}
		if oldestName != "" {
			delete(names, oldestName)
		}
	}
	names[name] = now
}

// hintForIP повторяет осторожную атрибуцию autolist hint_only: одно имя —
// используем его; несколько имён одного регистрируемого домена — домен;
// разные домены на одном адресе — неоднозначно, кандидата не создаём.
func (o *Observer) hintForIP(ip string, now time.Time) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	names := o.hints[ip]
	first, domain := "", ""
	for name, at := range names {
		if now.Sub(at) > HintTTLSec*time.Second {
			delete(names, name)
			continue
		}
		if first == "" {
			first = name
			domain = registeredDomain(name)
			continue
		}
		other := registeredDomain(name)
		if domain == "" || other == "" || other != domain {
			return ""
		}
	}
	if len(names) == 0 {
		delete(o.hints, ip)
		return ""
	}
	if first != "" && len(names) > 1 {
		return domain
	}
	return first
}

// logEnvelope — формат GET /api/logs (см. internal/api/logging.go LogsResponse).
type logEnvelope struct {
	Logs []logEntry `json:"logs"`
}

type logEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"`
	Group     string    `json:"group"`
	Subgroup  string    `json:"subgroup"`
	Message   string    `json:"message"`
}

var dialRe = compileDialRe()

// PollLogs считает только direct dial timeout на TCP/443 как аналог
// no-connect у autolist: три отказа домена за 60 секунд. Connection refused,
// no route и ошибки уже маршрутизированных outbounds блокировками не считаются.
func (o *Observer) PollLogs(ctx context.Context) []Observation {
	q := "/api/logs?bucket=singbox&level=error&limit=500&sanitize=false"
	if o.lastLogTS != "" {
		q += "&since=" + urlQueryEscape(o.lastLogTS)
	}
	body, err := o.get(ctx, q)
	if err != nil {
		return nil
	}
	var env logEnvelope
	if json.Unmarshal(body, &env) != nil {
		return nil
	}
	now := o.now()
	var last time.Time
	if o.lastLogTS != "" {
		last, _ = time.Parse(time.RFC3339Nano, o.lastLogTS)
	}
	previousLast, batchLast := last, last
	var out []Observation
	for _, e := range env.Logs {
		// Дедупликация относится к прошлым опросам. Одинаковые timestamp у
		// РАЗНЫХ строк одной пачки легальны и не должны съедать отказы.
		if !e.Timestamp.IsZero() && !previousLast.IsZero() && !e.Timestamp.After(previousLast) {
			continue
		}
		if e.Timestamp.After(batchLast) {
			batchLast = e.Timestamp
		}
		if !strings.Contains(e.Message, "i/o timeout") {
			continue
		}
		m := dialRe.FindStringSubmatch(e.Message)
		if m == nil {
			continue
		}
		host, port := m[1], m[3] // m[1] — IPv6 в скобках, m[2] — host/domain, m[3] — порт
		if host == "" {
			host = m[2]
		}
		if port != "443" {
			continue
		}
		// Отказ через туннель — к вопросу о смене сервера, а не о direct-сайте.
		if !strings.Contains(e.Message, "outbound/direct") {
			continue
		}
		h := Host{}
		if net.ParseIP(host) != nil {
			h.IP = host
			h.Name = o.hintForIP(host, now)
		} else {
			h.Name = strings.ToLower(strings.TrimSuffix(host, "."))
		}
		if privateIP(h.IP) && h.Name == "" {
			continue
		}
		if h.Name == "" || net.ParseIP(h.Name) != nil || strings.Contains(h.Name, ":") || registeredDomain(h.Name) == "" {
			continue
		}
		if bump(o.noConnect, observationKey(h), o.failureThreshold(), DefaultFailWindow*time.Second, now) {
			out = append(out, Observation{Host: h, Why: WhyNoConnect})
		}
	}
	if batchLast.After(last) {
		o.lastLogTS = batchLast.UTC().Format(time.RFC3339Nano)
	}
	return out
}

// bump — tally_add/notes_add из autolist: окно от последнего события и рестарт
// после номинации. Для stall threshold=2 это даёт ровно две FIN-ноты за 120 с.
func bump(m map[string]*tally, key string, threshold int, window time.Duration, now time.Time) bool {
	t := m[key]
	if t == nil && len(m) >= HostsCap {
		oldest := ""
		var oldestAt time.Time
		for candidate, item := range m {
			if oldest == "" || item.last.Before(oldestAt) {
				oldest, oldestAt = candidate, item.last
			}
		}
		if oldest != "" {
			delete(m, oldest)
		}
	}
	if t == nil || (!t.last.IsZero() && now.Sub(t.last) > window) {
		t = &tally{}
		m[key] = t
	}
	t.n++
	t.last = now
	if t.n < threshold {
		return false
	}
	delete(m, key)
	return true
}

func (o *Observer) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.base+path, nil)
	if err != nil {
		return nil, err
	}
	if o.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.apiKey)
	}
	resp, err := o.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", path, resp.StatusCode)
	}
	var buf strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		buf.WriteString(sc.Text())
	}
	return []byte(buf.String()), nil
}

func urlQueryEscape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "+", "%2B"), ":", "%3A")
}

// compileDialRe выделяет host:port из dial-ошибок sing-box. Хост — IPv4,
// IPv6 в скобках [::1]:80 или домен; порт — последнее :NNNN до пробела.
// Домен/IPv4 — нежадно, чтобы «1.2.3.4:5678:443» не съел порт в группу хоста.
func compileDialRe() *regexp.Regexp {
	return regexp.MustCompile(`dial (?:tcp|udp) (?:\[(.+?)\]|([A-Za-z0-9._-]+?)):(\d+)`)
}
