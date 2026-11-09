package autolist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Table — состояние кандидатов (аналог autolist candidates.c autolist_al_cands):
// домен, хост-источник, причина, состояние, счётчики. Dismiss прячет на сутки.
type Table struct {
	mu        sync.Mutex
	rows      map[string]*Candidate
	dismissed map[string]time.Time
	seq       uint64
	order     map[string]uint64
}

// Config — настройки сателлита, переживают рестарт (JSON на диске).
type Config struct {
	// AutoAdd — добавлять правило без вопроса пользователю после вердикта blocked.
	AutoAdd bool `json:"autoAdd"`
	// TargetInterface — kernel-интерфейс, общий для tunnel-пробы и правил.
	TargetInterface string `json:"targetInterface,omitempty"`
	// TargetOutbound — legacy-конфиг до перехода на интерфейсы. Не используется
	// для новых проб/правил; существующий rule-set при чтении не изменяется.
	TargetOutbound string `json:"targetOutbound,omitempty"`
	// ResolveVia — откуда резолвить домены при проверке покрытия по IP:
	//   "" (по умолчанию) — через awg-manager GET /routing/resolve (резолвит
	//     так, как его видит сам роутер, со всеми его DNS-настройками);
	//   "dns://host:port" или "8.8.8.8:53" — свой DNS-сервер (udp);
	//   "system" — системный резолвер машины, где крутится сателлит.
	// Настройка нужна потому, что IP-покрытие (ip_cidr-правила, geoip-наборы)
	// зависит от того, ЧТО вернёт резолв: CDN отдаёт разные адреса разным
	// резолверам, и проверка «не в geoip-ли он» обязана резолвить так же,
	// как это будет делать реальный клиент.
	ResolveVia string `json:"resolveVia,omitempty"`
	// ResolveIPEnabled — проверять ли покрытие по IP из резолва домена
	// (ip_cidr-правила и geoip-наборы). По умолчанию включено (true при
	// нулевом значении).
	ResolveIPEnabled *bool `json:"resolveIPEnabled,omitempty"`
	// ProbeInterface — kernel interface прямых проб; пусто — default route.
	ProbeInterface string `json:"probeInterface,omitempty"`
	// Verbose — подробные переходы наблюдения и очереди в журнале сателлита.
	Verbose bool `json:"verbose,omitempty"`
	// PacketEnabled — пакетные стратегии autolist через AF_PACKET; false — fallback.
	PacketEnabled bool `json:"packetEnabled,omitempty"`
	// Instant — первый доказанный отказ сразу создаёт кандидата и запускает пробу.
	Instant bool `json:"instant,omitempty"`
	// TunnelProbe — после direct blocked проверить HTTP через выбранный outbound.
	TunnelProbe bool `json:"tunnelProbe,omitempty"`
	// WANCheckURL — независимая проверка для no-connect; пусто — Cloudflare 204.
	WANCheckURL string `json:"wanCheckUrl,omitempty"`
	// ExcludeDomains — ручные постоянные исключения из номинаций.
	ExcludeDomains []string `json:"excludeDomains,omitempty"`
	// ExcludeRuleSets — дополнительные geosite/rule-set исключения.
	ExcludeRuleSets []string `json:"excludeRuleSets,omitempty"`
}

// ResolveIPOn — ResolveIPEnabled с умным дефолтом: nil = включено.
func (c Config) ResolveIPOn() bool {
	return c.ResolveIPEnabled == nil || *c.ResolveIPEnabled
}

// Candidate — строка таблицы.
type Candidate struct {
	Domain  string    `json:"domain"`
	Host    string    `json:"host"`
	IP      string    `json:"ip,omitempty"` // адрес наблюдаемого direct-соединения
	Why     string    `json:"why"`
	State   string    `json:"state"`
	Fails   int       `json:"fails"`
	Sources []string  `json:"sources"` // какие сборщики привели: dial|stall
	LastAt  time.Time `json:"lastAt"`
	// Verdict/ProbedAt — последний вердикт активной пробы.
	Verdict     string    `json:"verdict,omitempty"`
	ProbedAt    time.Time `json:"probedAt,omitempty"`
	Stage       string    `json:"stage,omitempty"`
	ProbeError  string    `json:"probeError,omitempty"`
	TunnelError string    `json:"tunnelError,omitempty"`
	// TunnelVerdict/TunnelProbedAt — отдельная необязательная проверка через outbound.
	TunnelVerdict  string    `json:"tunnelVerdict,omitempty"`
	TunnelProbedAt time.Time `json:"tunnelProbedAt,omitempty"`
}

func NewTable() *Table {
	return &Table{
		rows:      map[string]*Candidate{},
		dismissed: map[string]time.Time{},
		order:     map[string]uint64{},
	}
}

// Note добавляет/освежает кандидата; true означает, что номинация принята
// (в том числе для существующей строки), false — домен не подходит или скрыт.
func (t *Table) Note(host Host, why Why, source string, now time.Time) bool {
	return t.noteDomain(host, registeredDomain(host.Name), why, source, now)
}

// noteDomain сохраняет домен, выбранный judge при доменной агрегации. Обычные
// Clash/log наблюдения по-прежнему выводят его из exact host через Note.
func (t *Table) noteDomain(host Host, domain string, why Why, source string, now time.Time) bool {
	domain = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(domain), "."))
	if domain == "" || host.Name == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if until, bad := t.dismissed[domain]; bad && now.Before(until) {
		return false
	}
	delete(t.dismissed, domain)
	c, ok := t.rows[domain]
	if !ok {
		if len(t.rows) >= HostsCap {
			return false
		}
		t.seq++
		c = &Candidate{Domain: domain}
		t.rows[domain] = c
		t.order[domain] = t.seq
	}
	c.Host = host.Name
	c.IP = host.IP
	if why != WhyNone {
		c.Why = why.String()
	}
	c.Fails++
	c.LastAt = now
	if !containsStr(c.Sources, source) {
		c.Sources = append(c.Sources, source)
	}
	if c.State == "" {
		c.State = StateCandidate.String()
		c.Stage = "queued"
	}
	return true
}

// isDismissed проверяет временное скрытие для probe-only событий, которые ещё
// не имеют строки кандидата и поэтому не проходят через Note.
func (t *Table) isDismissed(domain string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	until, ok := t.dismissed[domain]
	if !ok {
		return false
	}
	if now.Before(until) {
		return true
	}
	delete(t.dismissed, domain)
	return false
}

// Confirm фиксирует вердикт пробы.
func (t *Table) Confirm(domain string, v Verdict, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	c, ok := t.rows[domain]
	if !ok {
		return
	}
	c.Verdict = v.String()
	c.ProbedAt = now
	c.Stage = ""
	// Tunnel-результат существует только внутри той же попытки. Если после
	// нового direct-вердикта tunnel не запускался, старое доказательство стирается.
	c.TunnelVerdict = ""
	c.TunnelProbedAt = time.Time{}
	c.TunnelError = ""
	switch v {
	case VerdictBlocked:
		c.State = StateCandidate.String()
	case VerdictReachable:
		if c.State == StateChecking.String() || c.State == StateCandidate.String() {
			delete(t.rows, domain)
			delete(t.order, domain)
		}
	default:
		// unknown не подтверждает блокировку. Строка снова может быть
		// номинирована наблюдателем без verdict cache.
		if c.State == StateChecking.String() {
			c.State = StateCandidate.String()
		}
	}
}

// ConfirmTunnel сохраняет отдельный результат проверки через туннель.
func (t *Table) ConfirmTunnel(domain string, verdict Verdict, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if c, ok := t.rows[domain]; ok {
		c.TunnelVerdict = verdict.String()
		c.TunnelProbedAt = now
	}
}

// ResetChecking завершает визуальный lifecycle отменённой/устаревшей пробы.
func (t *Table) ResetChecking(domain string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if c, ok := t.rows[domain]; ok {
		if c.State == StateChecking.String() {
			c.State = StateCandidate.String()
		}
		c.Stage = ""
	}
}

// ClearQueued снимает визуальный queued-stage при вытеснении задачи.
func (t *Table) ClearQueued(domain string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if c, ok := t.rows[domain]; ok && c.Stage == "queued" {
		c.Stage = ""
	}
}

// MarkChecking отмечает, что для номинации выполняется активная проба.
func (t *Table) MarkChecking(domain string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if c, ok := t.rows[domain]; ok {
		c.State = StateChecking.String()
		c.Stage = "direct"
		c.Verdict = ""
		c.ProbedAt = time.Time{}
		c.ProbeError = ""
		c.TunnelVerdict = ""
		c.TunnelProbedAt = time.Time{}
		c.TunnelError = ""
	}
}

// MarkTunnelChecking отличает вторую свежую пробу от direct в UI.
func (t *Table) MarkTunnelChecking(domain string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if c, ok := t.rows[domain]; ok {
		c.Stage = "tunnel"
	}
}

// SetProbeDiagnostics сохраняет причины текущих попыток только в памяти.
func (t *Table) SetProbeDiagnostics(domain, directErr, tunnelErr string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if c, ok := t.rows[domain]; ok {
		c.ProbeError = directErr
		c.TunnelError = tunnelErr
	}
}

// Dismiss прячет кандидата на сутки.
func (t *Table) Dismiss(domain string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.rows, domain)
	delete(t.order, domain)
	if _, exists := t.dismissed[domain]; !exists && len(t.dismissed) >= HostsCap {
		oldest := ""
		var until time.Time
		for d, at := range t.dismissed {
			if oldest == "" || at.Before(until) {
				oldest, until = d, at
			}
		}
		if oldest != "" {
			delete(t.dismissed, oldest)
		}
	}
	t.dismissed[domain] = now.Add(DismissSec * time.Second)
}

// RestoreDismiss отменяет временное скрытие. Существующая строка не
// восстанавливается: домен снова сможет появиться по новому наблюдению.
func (t *Table) RestoreDismiss(domain string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.dismissed, domain)
}

// Forget убирает кандидата без «чёрного списка».
func (t *Table) Forget(domain string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.rows, domain)
	delete(t.order, domain)
}

// get возвращает снимок строки по доменному ключу таблицы.
func (t *Table) get(domain string) (Candidate, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	c, ok := t.rows[domain]
	if !ok {
		return Candidate{}, false
	}
	out := *c
	out.Sources = append([]string(nil), c.Sources...)
	return out, true
}

// List возвращает кандидатов в порядке появления.
func (t *Table) List() []Candidate {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Candidate, 0, len(t.rows))
	for _, c := range t.rows {
		copyCandidate := *c
		copyCandidate.Sources = append([]string(nil), c.Sources...)
		out = append(out, copyCandidate)
	}
	sort.Slice(out, func(i, j int) bool {
		return t.order[out[i].Domain] < t.order[out[j].Domain]
	})
	return out
}

// --- публичный API awg-manager ---------------------------------------------

// RuleClient правит ЕДИНЫЙ inline rule-set сателлита и его правило-владелец
// через эндпоинты awg-manager (internal/api/singbox_router_rules.go,
// singbox_router_staging.go). Все домены кандидатов живут в одном наборе
// `awgm-autolist`; правило domain_suffix/autolist → outbound одно.
// Контракты сверены с исходниками 2.19.25:
//   - GET  /api/singbox/router/rules/list    -> {success,data:[RuleDTO]}
//   - GET  /api/singbox/router/rulesets/list -> {success,data:[RuleSetDTO]}
//   - POST /api/singbox/router/rulesets/add   -> RuleSet (tag,type,rules)
//   - POST /api/singbox/router/rulesets/update-> {tag, ruleSet:{...}}
//   - POST /api/singbox/router/rules/add      -> router.Rule
//   - POST /api/singbox/router/staging/apply.
type RuleClient struct {
	base   string
	apiKey string
	http   *http.Client
}

func NewRuleClient(base, apiKey string) *RuleClient {
	return &RuleClient{
		base:   strings.TrimRight(base, "/"),
		apiKey: apiKey,
		http:   &http.Client{Timeout: 20 * time.Second},
	}
}

// LANBridges discovers only up, private NDMS Bridge interfaces. Name is the
// kernel ifname (for example br0), used by AF_PACKET capture.
func (r *RuleClient) LANBridges(ctx context.Context) ([]string, error) {
	body, err := r.get(ctx, "/api/system/all-interfaces")
	if err != nil {
		return nil, err
	}
	var env struct {
		Success bool `json:"success"`
		Data    []struct {
			Name          string `json:"name"`
			Up            bool   `json:"up"`
			Type          string `json:"type"`
			SecurityLevel string `json:"securityLevel"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, err
	}
	if !env.Success {
		return nil, errors.New("all-interfaces: unsuccessful response")
	}
	interfaces := make([]string, 0, len(env.Data))
	for _, iface := range env.Data {
		if iface.Up && strings.EqualFold(iface.Type, "Bridge") && strings.EqualFold(iface.SecurityLevel, "private") {
			interfaces = append(interfaces, iface.Name)
		}
	}
	return interfaces, nil
}

// ruleDTO — SingboxRouterRuleDTO (internal/api/singbox_router_dto.go:196).
type ruleDTO struct {
	DomainSuffix []string `json:"domain_suffix,omitempty"`
	Domain       []string `json:"domain,omitempty"`
	IPCIDR       []string `json:"ip_cidr,omitempty"`
	Port         []int    `json:"port,omitempty"`
	RuleSet      []string `json:"rule_set,omitempty"`
	Protocol     string   `json:"protocol,omitempty"`
	Action       string   `json:"action,omitempty"`
	Outbound     string   `json:"outbound,omitempty"`
	AwgmManaged  string   `json:"awgm_managed,omitempty"`
}

// ruleSetTag — тег единого набора правил автосписка (один список на всё).
const ruleSetTag = "awgm-autolist"

// apiEnvelope — конверт ответов awg-manager.
type apiEnvelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
}

// InspectResult — ответ POST /singbox/router/inspect (singbox_inspector.go).
type InspectResult struct {
	Input     string `json:"input"`
	InputType string `json:"inputType"`
	Matches   []struct {
		Index      int      `json:"index"`
		Matched    bool     `json:"matched"`
		Action     string   `json:"action"`
		Outbound   string   `json:"outbound"`
		Conditions []string `json:"conditions"`
		Reason     string   `json:"reason"`
	} `json:"matches"`
	Destination string `json:"destination"`
	MatchedRule int    `json:"matchedRule"`
	Final       string `json:"final"`
	Note        string `json:"note"`
}

// inspect отправляет домен или IP в симулятор правил роутера. Ошибка сети —
// (nil, err); валидный ответ — (result, nil), даже когда ничего не совпало.
// sing-box сам различает домен и IP во входе (inspector.go: net.ParseIP),
// так что один метод покрывает и domain/list-правила, и ip_cidr/geoip.
func (r *RuleClient) Inspect(ctx context.Context, domainOrIP string) (*InspectResult, error) {
	body, err := r.post(ctx, "/api/singbox/router/inspect", map[string]any{
		"domain": domainOrIP,
	})
	if err != nil {
		return nil, err
	}
	var env struct {
		Success bool          `json:"success"`
		Data    InspectResult `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, err
	}
	return &env.Data, nil
}

// ResolveDomain резолвит домен так, как его видит роутер:
//   - cfgResolveVia пуст или "awgm" — GET /routing/resolve awg-manager
//     (роутер резолвит своими DNS, со всеми его настройками);
//   - "system" — системный резолвер сателлита;
//   - "host:port" или "dns://host:port" — указанный UDP-DNS.
func (r *RuleClient) ResolveDomain(ctx context.Context, domain, cfgResolveVia string) ([]string, error) {
	via := strings.TrimSpace(cfgResolveVia)
	switch {
	case via == "" || via == "awgm":
		body, err := r.get(ctx, "/api/routing/resolve?domain="+url.QueryEscape(domain))
		if err != nil {
			return nil, err
		}
		var env struct {
			Success bool `json:"success"`
			Data    struct {
				IPs   []string `json:"ips"`
				Error string   `json:"error,omitempty"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &env); err != nil {
			return nil, err
		}
		if env.Data.Error != "" {
			return nil, errors.New(env.Data.Error)
		}
		return env.Data.IPs, nil
	case via == "system":
		addrs, err := net.DefaultResolver.LookupHost(ctx, domain)
		if err != nil {
			return nil, err
		}
		return addrs, nil
	case strings.HasPrefix(via, "sb:"):
		return r.ResolveSingboxDomain(ctx, domain, via)
	default:
		via = strings.TrimPrefix(via, "dns://")
		host, port, err := net.SplitHostPort(via)
		if err != nil {
			return nil, fmt.Errorf("resolveVia: ждём host:port, system или awgm: %w", err)
		}
		if port == "" {
			port = "53"
		}
		rd := &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				d := net.Dialer{Timeout: 5 * time.Second}
				return d.DialContext(ctx, network, net.JoinHostPort(host, port))
			},
		}
		addrs, err := rd.LookupHost(ctx, domain)
		if err != nil {
			return nil, err
		}
		return addrs, nil
	}
}

// HasRoutingDecision сообщает о любом терминальном пользовательском решении,
// включая явный direct и reject. Финальный default direct без совпавшего правила
// решением не считается и допускает обычный конвейер автосписка.
func (res *InspectResult) HasRoutingDecision() bool {
	return res != nil && res.MatchedRule >= 0
}

// ruleView — плоский вид правила из rules/list для поиска и disable.
type ruleView struct {
	Index        int            `json:"index"`
	Match        string         `json:"match"`
	Action       string         `json:"action,omitempty"`
	Outbound     string         `json:"outbound,omitempty"`
	Managed      bool           `json:"managed"`
	DomainSuffix []string       `json:"domainSuffix,omitempty"`
	Domain       []string       `json:"domain,omitempty"`
	RuleSet      []string       `json:"ruleSet,omitempty"`
	Raw          map[string]any `json:"-"`
}

const autolistTargetTag = "awgm-autolist-target"

// ensureInterfaceOutbound создаёт собственный direct outbound с привязкой к
// выбранному kernel-интерфейсу. Тот же интерфейс использует полноценная проба.
func (r *RuleClient) ensureInterfaceOutbound(ctx context.Context, iface string) error {
	if iface == "" || strings.ContainsAny(iface, " \t\n") {
		return errors.New("target interface is empty or invalid")
	}
	body, err := r.get(ctx, "/api/singbox/router/outbounds/list")
	if err != nil {
		return fmt.Errorf("outbounds list: %w", err)
	}
	var env struct {
		Data []struct {
			Tag           string `json:"tag"`
			Type          string `json:"type"`
			BindInterface string `json:"bind_interface"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("outbounds list: %w", err)
	}
	target := map[string]any{"tag": autolistTargetTag, "type": "direct", "bind_interface": iface}
	for _, ob := range env.Data {
		if ob.Tag != autolistTargetTag {
			continue
		}
		if ob.Type != "direct" {
			return fmt.Errorf("outbound %q занят типом %q", autolistTargetTag, ob.Type)
		}
		if ob.BindInterface == iface {
			return nil
		}
		_, err := r.post(ctx, "/api/singbox/router/outbounds/update", map[string]any{
			"tag": autolistTargetTag, "outbound": target,
		})
		return err
	}
	_, err = r.post(ctx, "/api/singbox/router/outbounds/add", target)
	return err
}

// unconditionalSuffixRule — собственная форма записи автосписка. Даже
// безобидное на вид дополнительное поле не даёт присоединять к объекту новые
// домены: это может быть условием, смысл которого сателлит не обязан знать.
func unconditionalSuffixRule(rule map[string]any) bool {
	if len(rule) != 1 {
		return false
	}
	_, ok := rule["domain_suffix"].([]any)
	return ok
}

// AddDomainSuffixRule добавляет домены в ЕДИНЫЙ rule-set awgm-autolist и
// применяет staging. Правило ведёт в direct outbound, привязанный к iface.
func (r *RuleClient) AddDomainSuffixRule(ctx context.Context, domains []string, iface string) error {
	if len(domains) == 0 {
		return nil
	}
	// 1. Текущее состояние набора (или его отсутствие).
	rs, exists, err := r.getRuleSet(ctx, ruleSetTag)
	if err != nil {
		return err
	}

	// 2. Сохраняем все существующие rule-объекты. Новые домены принадлежат
	// только собственному безусловному suffix-list, а не чужому UDP/client-scoped
	// или иначе ограниченному объекту.
	newRules := []map[string]any{}
	if exists {
		raw, err := json.Marshal(rs.Rules)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &newRules); err != nil {
			return err
		}
	}
	current := map[string]bool{}
	targetRule := -1
	for i, rule := range newRules {
		list, ok := rule["domain_suffix"].([]any)
		if !ok || !unconditionalSuffixRule(rule) {
			continue
		}
		if targetRule < 0 {
			targetRule = i
		}
		for _, v := range list {
			if s, ok := v.(string); ok {
				current[s] = true
			}
		}
	}
	var fresh []string
	for _, d := range domains {
		if !current[d] {
			current[d] = true
			fresh = append(fresh, d)
		}
	}
	if len(fresh) == 0 {
		return nil // все уже в наборе
	}
	if err := r.ensureInterfaceOutbound(ctx, iface); err != nil {
		return fmt.Errorf("target outbound: %w", err)
	}
	if targetRule < 0 {
		newRules = append(newRules, map[string]any{"domain_suffix": fresh})
	} else {
		list := newRules[targetRule]["domain_suffix"].([]any)
		for _, d := range fresh {
			list = append(list, d)
		}
		sort.Slice(list, func(i, j int) bool { return fmt.Sprint(list[i]) < fmt.Sprint(list[j]) })
		newRules[targetRule]["domain_suffix"] = list
	}

	// 3. Создание или обновление набора.
	if !exists {
		payload := map[string]any{
			"tag":   ruleSetTag,
			"type":  "inline",
			"rules": newRules,
		}
		if _, err := r.post(ctx, "/api/singbox/router/rulesets/add", payload); err != nil {
			return fmt.Errorf("ruleset create: %w", err)
		}
	} else {
		payload := map[string]any{
			"tag":     ruleSetTag,
			"ruleSet": map[string]any{"tag": ruleSetTag, "type": "inline", "rules": newRules},
		}
		if _, err := r.post(ctx, "/api/singbox/router/rulesets/update", payload); err != nil {
			return fmt.Errorf("ruleset update: %w", err)
		}
	}

	// 4. Правило-владелец: rule_set=[awgm-autolist] → interface-bound outbound.
	if err := r.ensureOwnerRule(ctx, autolistTargetTag); err != nil {
		return err
	}

	// 5. Применяем staging одной операцией.
	if err := r.applyStaging(ctx); err != nil {
		return fmt.Errorf("набор обновлён (в черновике), но staging не применён: %w", err)
	}
	return nil
}

// rulesetDTO — SingboxRouterRuleSetDTO (singbox_router_dto.go).
type rulesetDTO struct {
	Tag   string           `json:"tag"`
	Type  string           `json:"type"`
	Rules []map[string]any `json:"rules,omitempty"`
}

// getRuleSet читает текущий inline набор сателлита.
func (r *RuleClient) getRuleSet(ctx context.Context, tag string) (rulesetDTO, bool, error) {
	body, err := r.get(ctx, "/api/singbox/router/rulesets/list")
	if err != nil {
		return rulesetDTO{}, false, err
	}
	var env struct {
		Success bool         `json:"success"`
		Data    []rulesetDTO `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return rulesetDTO{}, false, err
	}
	for _, rs := range env.Data {
		if rs.Tag == tag {
			return rs, true, nil
		}
	}
	return rulesetDTO{}, false, nil
}

// ensureOwnerRule — точное правило rule_set=[awgm-autolist] → outbound.
// Правило, которое одновременно использует другие наборы, не принадлежит только
// автосписку и не изменяется. У собственного правила обновляются лишь его три
// управляемых поля; все пользовательские ограничения сохраняются.
func (r *RuleClient) ensureOwnerRule(ctx context.Context, outbound string) error {
	views, err := r.listRules(ctx)
	if err != nil {
		return err
	}
	for _, v := range views {
		if len(v.RuleSet) != 1 || v.RuleSet[0] != ruleSetTag || (v.Action != "" && v.Action != "route") {
			continue
		}
		if v.Outbound == outbound {
			return nil // уже как надо
		}
		rule := map[string]any{}
		for key, value := range v.Raw {
			rule[key] = value
		}
		// Поля плоского MCP view не входят в router.Rule.
		delete(rule, "index")
		delete(rule, "match")
		delete(rule, "managed")
		rule["rule_set"] = []string{ruleSetTag}
		rule["action"] = "route"
		rule["outbound"] = outbound
		_, err := r.post(ctx, "/api/singbox/router/rules/update", map[string]any{
			"index": v.Index,
			"rule":  rule,
		})
		return err
	}
	_, err = r.post(ctx, "/api/singbox/router/rules/add", map[string]any{
		"rule_set": []string{ruleSetTag},
		"action":   "route",
		"outbound": outbound,
	})
	return err
}

// ListDomains возвращает домены из набора (для сверки таблицы).
func (r *RuleClient) ListDomains(ctx context.Context) ([]string, error) {
	rs, exists, err := r.getRuleSet(ctx, ruleSetTag)
	if err != nil || !exists {
		return nil, err
	}
	var out []string
	for _, rule := range rs.Rules {
		if list, ok := rule["domain_suffix"].([]any); ok && unconditionalSuffixRule(rule) {
			for _, v := range list {
				if s, ok := v.(string); ok {
					out = append(out, s)
				}
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// applyStaging применяет черновик роутера. Пустой черновик/409 «нечего
// применять» трактуем как успех — правило уже могло быть применено ранее.
func (r *RuleClient) applyStaging(ctx context.Context) error {
	_, err := r.post(ctx, "/api/singbox/router/staging/apply", map[string]any{})
	// Изменение только содержимого inline-набора уже hot-reload'нуло live SRS;
	// draft при этом отсутствует. Это успех, а не ошибка добавления.
	if err != nil && strings.Contains(err.Error(), `"code":"NO_DRAFT"`) {
		return nil
	}
	return err
}

// listRules читает rules/list и разворачивает плоские view, сохраняя исходный
// объект для последующего точечного update. В текущем AWGM data приходит как
// SingboxRouterRuleDTO (snake_case); старый camelCase view остаётся поддержанным.
func (r *RuleClient) listRules(ctx context.Context) ([]ruleView, error) {
	body, err := r.get(ctx, "/api/singbox/router/rules/list")
	if err != nil {
		return nil, err
	}
	var env apiEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, err
	}
	var rawRules []map[string]any
	if err := json.Unmarshal(env.Data, &rawRules); err != nil {
		return nil, fmt.Errorf("rules/list: непонятный формат ответа")
	}
	views := make([]ruleView, 0, len(rawRules))
	for i, raw := range rawRules {
		encoded, err := json.Marshal(raw)
		if err != nil {
			return nil, err
		}
		var dto ruleDTO
		var flat ruleView
		if err := json.Unmarshal(encoded, &dto); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(encoded, &flat); err != nil {
			return nil, err
		}
		view := ruleView{
			Index: i, Action: dto.Action, Outbound: dto.Outbound,
			Managed:      dto.AwgmManaged != "" || flat.Managed,
			DomainSuffix: dto.DomainSuffix, Domain: dto.Domain, RuleSet: dto.RuleSet,
			Raw: raw,
		}
		if _, ok := raw["index"]; ok {
			view.Index = flat.Index
		}
		if len(view.RuleSet) == 0 {
			view.RuleSet = flat.RuleSet
		}
		if len(view.DomainSuffix) == 0 {
			view.DomainSuffix = flat.DomainSuffix
		}
		views = append(views, view)
	}
	return views, nil
}

func (r *RuleClient) get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.base+path, nil)
	if err != nil {
		return nil, err
	}
	if r.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.apiKey)
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: статус %d", path, resp.StatusCode)
	}
	return readAll(resp.Body)
}

func (r *RuleClient) post(ctx context.Context, path string, payload map[string]any) ([]byte, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.base+path, strings.NewReader(string(b)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if r.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.apiKey)
	}
	resp, err := r.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := readAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("POST %s: статус %d: %s", path, resp.StatusCode, cut(string(body), 240))
	}
	return body, nil
}

func readAll(rd io.Reader) ([]byte, error) {
	return io.ReadAll(io.LimitReader(rd, 8<<20))
}

func containsStr(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
