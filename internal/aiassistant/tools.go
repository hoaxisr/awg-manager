package aiassistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	sysexec "github.com/hoaxisr/awg-manager/internal/sys/exec"
)

type ToolCall struct {
	Name      string            `json:"name"`
	Arguments map[string]string `json:"arguments,omitempty"`
}

// ToolDefinition is the transport-neutral subset of an MCP tool definition.
// The same catalog is used by the built-in chat agent and the HTTP MCP
// endpoint, so adding a router capability cannot silently create two
// different permission models.
type ToolDefinition struct {
	Name        string         `json:"name"`
	Title       string         `json:"title"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	ReadOnly    bool           `json:"readOnly"`
	Risk        string         `json:"risk"`
}

type ToolStep struct {
	Name       string        `json:"name"`
	Title      string        `json:"title"`
	Status     string        `json:"status"`
	Summary    string        `json:"summary"`
	Evidence   []string      `json:"evidence,omitempty"`
	DurationMS int64         `json:"durationMs"`
	ReadOnly   bool          `json:"readOnly"`
	StartedAt  time.Time     `json:"startedAt"`
	Duration   time.Duration `json:"-"`
}

type ToolExecutor interface {
	Plan(string) (Intent, []ToolCall)
	Execute(context.Context, ToolCall) ToolStep
}

type ToolCatalog interface {
	Tools() []ToolDefinition
}

type hostResolver interface {
	LookupHost(context.Context, string) ([]string, error)
}

type commandRunner func(context.Context, string, ...string) (*sysexec.Result, error)

type ToolRegistry struct {
	resolver hostResolver
	run      commandRunner
	sources  ToolSources
	memory   *MemoryStore
}

// ToolSources bridges the assistant to AWG Manager's existing Go services.
// Callbacks return deliberately reduced, secret-free views assembled by the
// application wiring; the tool layer never reads service storage directly.
type ToolSources struct {
	FullDiagnostics func(context.Context) (any, error)
	SystemSnapshot  func(context.Context) (any, error)
	SystemServices  func(context.Context) (any, error)
	SystemPorts     func(context.Context) (any, error)
	SystemPackages  func(context.Context, string, string) (any, error)
	SystemFilesList func(context.Context, string) (any, error)
	SystemFileRead  func(context.Context, string) (any, error)
	EngineStatus    func(context.Context) (any, error)
	Tunnels         func(context.Context) (any, error)
	TunnelStatus    func(context.Context, string) (any, error)
	Subscriptions   func(context.Context) (any, error)
	Connections     func(context.Context, string, int) (any, error)
	ExplainClient   func(context.Context, string) (any, error)
	Logs            func(context.Context, string, string, string, int) (any, error)
	ProxyGroups     func(context.Context, string) (any, error)
	InspectRule     func(context.Context, string, int, string) (any, error)
	TestOutbound    func(context.Context, string, string) (any, error)
	ExplainDNS      func(context.Context, string, string, string) (any, error)
	ExplainConn     func(context.Context, string, int, string, int, string) (any, error)
	DNSProbe        func(context.Context) error
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{resolver: net.DefaultResolver, run: sysexec.Run}
}

func NewToolRegistryWithSources(sources ToolSources) *ToolRegistry {
	registry := NewToolRegistry()
	registry.sources = sources
	return registry
}

func (r *ToolRegistry) SetMemoryStore(m *MemoryStore) {
	r.memory = m
}

func (r *ToolRegistry) MemoryStore() *MemoryStore {
	return r.memory
}

func (r *ToolRegistry) Tools() []ToolDefinition {
	empty := map[string]any{"type": "object", "additionalProperties": false}
	return []ToolDefinition{
		{
			Name: "system.snapshot", Title: "Состояние роутера",
			Description: "Возвращает загрузку CPU, память, uptime и безопасный список процессов без командных строк и секретов.",
			InputSchema: cloneSchema(empty), ReadOnly: true, Risk: "none",
		},
		{
			Name: "system.services", Title: "Сервисы Entware",
			Description: "Показывает init.d-сервисы Entware, автозапуск, runtime-состояние и признак критического управляемого сервиса.",
			InputSchema: cloneSchema(empty), ReadOnly: true, Risk: "none",
		},
		{
			Name: "system.ports", Title: "Открытые порты",
			Description: "Показывает слушающие TCP/UDP-порты и связанные процессы без полных командных строк.",
			InputSchema: cloneSchema(empty), ReadOnly: true, Risk: "none",
		},
		{
			Name: "system.packages", Title: "Пакеты Entware",
			Description: "Показывает установленные или обновляемые пакеты opkg либо выполняет поиск пакета. Не изменяет систему.",
			InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"kind":  map[string]any{"type": "string", "enum": []string{"installed", "upgradable", "search"}},
				"query": map[string]any{"type": "string", "description": "Строка поиска; нужна только для kind=search"},
			}, "required": []string{"kind"}}, ReadOnly: true, Risk: "none",
		},
		{
			Name: "system.files.list", Title: "Файлы Entware",
			Description: "Показывает содержимое каталога только внутри разрешённых корней файлового менеджера AWG Manager. Не читает содержимое файлов.",
			InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Абсолютный путь внутри /opt или /tmp"},
			}, "required": []string{"path"}}, ReadOnly: true, Risk: "none",
		},
		{
			Name: "system.file.read_safe", Title: "Прочитать файл безопасно",
			Description: "Читает небольшой текстовый файл внутри разрешённых корней AWG Manager. Строки с паролями, токенами, API-ключами и приватными ключами маскируются до передачи модели.",
			InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"path": map[string]any{"type": "string", "description": "Абсолютный путь к текстовому файлу внутри /opt или /tmp"},
			}, "required": []string{"path"}}, ReadOnly: true, Risk: "none",
		},
		{
			Name: "diagnostics.full", Title: "Полная диагностика AWG Manager",
			Description: "Запускает штатный безопасный набор диагностических проверок AWG Manager и возвращает структурированный отчёт. Используй, когда вопрос широкий или причина неизвестна; для точечного вопроса сначала предпочитай специализированные инструменты.",
			InputSchema: cloneSchema(empty), ReadOnly: true, Risk: "none",
		},
		{
			Name: "domain.inspect", Title: "Проверить домен и маршрут",
			Description: "Разрешает домен через DNS роутера и показывает маршрут ядра до полученных IP-адресов.",
			InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"domain": map[string]any{"type": "string", "description": "Домен без URL, например youtube.com"},
			}, "required": []string{"domain"}}, ReadOnly: true, Risk: "none",
		},
		{
			Name: "routing.snapshot", Title: "Снимок маршрутизации",
			Description: "Показывает policy rules и таблицы маршрутизации Linux. Используй при пропаже интернета, утечках и неверном выходе через туннель.",
			InputSchema: cloneSchema(empty), ReadOnly: true, Risk: "none",
		},
		{
			Name: "dns.inspect", Title: "Проверить DNS",
			Description: "Проверяет разрешение указанного домена системным DNS роутера.",
			InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"domain": map[string]any{"type": "string", "description": "Необязательный тестовый домен"},
			}}, ReadOnly: true, Risk: "none",
		},
		{
			Name: "process.inspect", Title: "Проверить процессы движков",
			Description: "Показывает только процессы AWG Manager, Sing-box, Mihomo и туннельных служб без полного списка процессов роутера.",
			InputSchema: cloneSchema(empty), ReadOnly: true, Risk: "none",
		},
		{
			Name: "engines.status", Title: "Состояние движков",
			Description: "Показывает выбранное ядро маршрутизации и фактическое состояние Sing-box и Mihomo через внутренние сервисы AWG Manager.",
			InputSchema: cloneSchema(empty), ReadOnly: true, Risk: "none",
		},
		{
			Name: "tunnels.list", Title: "Список туннелей",
			Description: "Возвращает безопасную сводку настроенных VPN-туннелей: имя, backend, интерфейс, включённость и runtime-состояние.",
			InputSchema: cloneSchema(empty), ReadOnly: true, Risk: "none",
		},
		{
			Name: "tunnels.status", Title: "Состояние туннеля",
			Description: "Показывает подробное runtime-состояние одного туннеля по ID без конфигурации, адресов и ключей.",
			InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"id": map[string]any{"type": "string", "description": "ID туннеля из tunnels.list"},
			}, "required": []string{"id"}}, ReadOnly: true, Risk: "none",
		},
		{
			Name: "subscriptions.status", Title: "Состояние подписок",
			Description: "Показывает подписки Sing-box и Mihomo, число серверов, активность, последнее обновление и ошибку без URL, заголовков и конфигурационных секретов.",
			InputSchema: cloneSchema(empty), ReadOnly: true, Risk: "none",
		},
		{
			Name: "connections.search", Title: "Поиск соединений",
			Description: "Ищет текущие соединения по IP, имени клиента или адресу назначения и показывает определённый маршрут/туннель.",
			InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"search": map[string]any{"type": "string", "description": "IP, имя клиента или адрес назначения; пустая строка показывает общую выборку"},
				"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "description": "Максимум записей, по умолчанию 20"},
			}}, ReadOnly: true, Risk: "none",
		},
		{
			Name: "routing.explain_client", Title: "Объяснить маршрут клиента",
			Description: "Сопоставляет клиентское устройство, NDMS-политику, персональный client-route и текущие соединения, чтобы объяснить фактический маршрут.",
			InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"client": map[string]any{"type": "string", "description": "IP, имя или hostname клиента"},
			}, "required": []string{"client"}}, ReadOnly: true, Risk: "none",
		},
		{
			Name: "logs.tail", Title: "Последние записи журнала",
			Description: "Читает последние записи внутренних журналов AWG Manager из разрешённых групп. Адреса и домены маскируются до передачи модели.",
			InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"bucket": map[string]any{"type": "string", "enum": []string{"app", "singbox", "mihomo"}, "description": "Буфер журнала"},
				"group":  map[string]any{"type": "string", "enum": []string{"tunnel", "routing", "server", "system", "singbox", "mihomo"}, "description": "Разрешённая группа; для журнала движка укажи одноимённую группу, пусто — все группы выбранного буфера"},
				"level":  map[string]any{"type": "string", "enum": []string{"error", "warn", "info", "full", "debug"}, "description": "Необязательный уровень"},
				"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "description": "Число последних записей, по умолчанию 30"},
			}, "required": []string{"bucket"}}, ReadOnly: true, Risk: "none",
		},
		{
			Name: "proxy.groups", Title: "Состояние proxy-групп",
			Description: "Показывает runtime proxy-группы, выбранные узлы и последние задержки через Clash API активного или указанного ядра.",
			InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"engine": map[string]any{"type": "string", "enum": []string{"auto", "singbox", "mihomo"}, "description": "Ядро; auto использует выбранное в маршрутизации"},
			}}, ReadOnly: true, Risk: "none",
		},
		{
			Name: "routing.explain_rule", Title: "Объяснить правило назначения",
			Description: "Прогоняет домен или IP через инспектор правил выбранного ядра и показывает первое терминальное совпадение и итоговый outbound.",
			InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"destination": map[string]any{"type": "string", "description": "Домен или IP назначения"},
				"port":        map[string]any{"type": "integer", "minimum": 0, "maximum": 65535, "description": "Порт назначения; 0 — не учитывать"},
				"protocol":    map[string]any{"type": "string", "enum": []string{"tcp", "udp"}, "description": "Транспорт; пусто — не учитывать"},
			}, "required": []string{"destination"}}, ReadOnly: true, Risk: "none",
		},
		{
			Name: "outbound.test", Title: "Проверить outbound",
			Description: "Проверяет задержку конкретного outbound через Clash API и фиксированный безопасный URL AWG Manager. Настройки не изменяются.",
			InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"name":   map[string]any{"type": "string", "description": "Точное имя outbound или узла из proxy.groups"},
				"engine": map[string]any{"type": "string", "enum": []string{"auto", "singbox", "mihomo"}, "description": "Ядро; auto использует выбранное"},
			}, "required": []string{"name"}}, ReadOnly: true, Risk: "none",
		},
		{
			Name: "dns.explain", Title: "Объяснить DNS-цепочку",
			Description: "Показывает совпавшее DNS-правило, выбранный resolver и режим ответа fakeip/real/local для домена в активном ядре.",
			InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"domain":    map[string]any{"type": "string", "description": "Домен для проверки"},
				"sourceIp":  map[string]any{"type": "string", "description": "Необязательный IP клиента для source_ip_cidr"},
				"queryType": map[string]any{"type": "string", "enum": []string{"A", "AAAA"}, "description": "Тип DNS-запроса"},
			}, "required": []string{"domain"}}, ReadOnly: true, Risk: "none",
		},
		{
			Name: "connections.explain", Title: "Объяснить live-соединение",
			Description: "Находит точное conntrack-соединение и рядом показывает результат инспектора правил для его назначения.",
			InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"source":          map[string]any{"type": "string", "description": "IP источника"},
				"sourcePort":      map[string]any{"type": "integer", "minimum": 0, "maximum": 65535},
				"destination":     map[string]any{"type": "string", "description": "IP назначения"},
				"destinationPort": map[string]any{"type": "integer", "minimum": 0, "maximum": 65535},
				"protocol":        map[string]any{"type": "string", "enum": []string{"tcp", "udp", "icmp"}},
			}, "required": []string{"source", "destination", "protocol"}}, ReadOnly: true, Risk: "none",
		},
		{
			Name: "system.diagnose_command", Title: "Диагностическая команда ОС",
			Description: "Выполняет строго проверенную диагностическую команду из белого списка в Linux/Entware роутера. Разрешены только безопасные команды сбора информации: ip route/rule/addr/link show, ping -c, traceroute, ss, netstat, dmesg, logread, cat (/proc, /sys), ps, top -b, free, df, nslookup, dig, iptables (инспекция -L/-S/-v/-n), nft list, awg/wg show, conntrack -L, curl (только чтение GET, без POST к RCI), /opt/etc/init.d/* status, opkg (list/status/info). Любые мутирующие операции (ndmc, ip add/del/set, POST к RCI, запись в файлы >, kill, reboot) строго заблокированы.",
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "Командная строка для выполнения (например: curl -4 -v -m 5 --interface opkgtun10 http://cp.cloudflare.com/generate_204, iptables -t mangle -nvL, dmesg | tail -n 30, ip route show table all, curl -s http://127.0.0.1:79/rci/show/interface, ping -c 3 -W 2 -I opkgtun10 1.1.1.1)",
					},
					"timeoutSeconds": map[string]any{
						"type":        "integer",
						"minimum":     1,
						"maximum":     15,
						"description": "Таймаут выполнения команды в секундах (по умолчанию 8)",
					},
				},
				"required": []string{"command"},
			},
			ReadOnly: true,
			Risk:     "none",
		},
		{
			Name: "remediation.propose", Title: "Предложить исправление",
			Description: "Формирует безопасное предложение штатного исправления. Ничего не применяет: действие появится в интерфейсе и потребует отдельного подтверждения пользователя.",
			InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"action": map[string]any{"type": "string", "enum": []string{"singbox.restart", "mihomo.restart", "mihomo.reload", "routing.reapply", "routing.switch_engine", "routing.switch_mode", "tunnel.restart", "subscription.update", "service.start", "service.stop", "service.restart", "opkg.update", "opkg.install", "opkg.upgrade", "opkg.remove", "keenetic.ndmc"}},
				"target": map[string]any{"type": "string", "description": "ID туннеля, имя сервиса или команда KeeneticOS (например: interface SSTP0 security-level private, ip route ...) для keenetic.ndmc; для действий без цели оставь пустым"},
			}, "required": []string{"action"}}, ReadOnly: true, Risk: "none",
		},
		{
			Name: "memory.learn_fact", Title: "Запомнить факт о сети или роутере",
			Description: "Сохраняет проверенный факт, топологическую особенность или предпочтение пользователя в долговременную память ассистента. Используй при обнаружении постоянных особенностей окружения, кастомных портов, интерфейсов, провайдеров или прямых указаниях пользователя.",
			InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"category": map[string]any{"type": "string", "enum": []string{"network", "routing", "device", "user_pref", "troubleshooting"}, "description": "Категория знания"},
				"key":      map[string]any{"type": "string", "description": "Короткий идентификатор или тема факта"},
				"fact":     map[string]any{"type": "string", "description": "Суть проверенного факта или знания"},
				"source":   map[string]any{"type": "string", "description": "Необязательный источник, по умолчанию ai_dialog"},
			}, "required": []string{"category", "key", "fact"}}, ReadOnly: true, Risk: "none",
		},
		{
			Name: "memory.save_playbook", Title: "Сохранить сценарий решения проблемы",
			Description: "Сохраняет успешный сценарий устранения неполадки (симптом -> диагноз -> рекомендуемое действие) в долговременную память ассистента для повторного использования ассистентом и фоновым часовым (Sentinel).",
			InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
				"symptom":           map[string]any{"type": "string", "description": "Краткое описание проблемы или симптома сбоя"},
				"diagnosis":         map[string]any{"type": "string", "description": "Установленная причина сбоя"},
				"remediationAction": map[string]any{"type": "string", "enum": []string{"singbox.restart", "mihomo.restart", "mihomo.reload", "routing.reapply", "routing.switch_engine", "routing.switch_mode", "tunnel.restart", "subscription.update", "service.start", "service.stop", "service.restart", "opkg.update", "opkg.install", "opkg.upgrade", "opkg.remove"}, "description": "Действие для устранения сбоя"},
				"remediationTarget": map[string]any{"type": "string", "description": "Цель действия (имя туннеля, сервиса или команда)"},
				"explanation":       map[string]any{"type": "string", "description": "Краткое объяснение, почему это действие помогает"},
			}, "required": []string{"symptom", "diagnosis", "remediationAction"}}, ReadOnly: true, Risk: "none",
		},
		{
			Name: "memory.list_facts", Title: "Просмотреть базу знаний",
			Description: "Возвращает сохранённые в долговременной памяти факты о роутере и проверенные сценарии решений (playbooks).",
			InputSchema: cloneSchema(empty), ReadOnly: true, Risk: "none",
		},
	}
}

func cloneSchema(schema map[string]any) map[string]any {
	raw, _ := json.Marshal(schema)
	var cloned map[string]any
	_ = json.Unmarshal(raw, &cloned)
	return cloned
}

func (r *ToolRegistry) Plan(question string) (Intent, []ToolCall) {
	intent := DetectIntent(question)
	switch intent.Kind {
	case "domain.inspect":
		return intent, []ToolCall{{Name: "domain.inspect", Arguments: intent.Entity}}
	case "tunnels.check":
		return intent, []ToolCall{{Name: "tunnels.list"}}
	case "dns.check":
		return intent, []ToolCall{{Name: "dns.inspect", Arguments: map[string]string{"domain": "google.com"}}}
	case "routing.check":
		return intent, []ToolCall{{Name: "engines.status"}, {Name: "routing.snapshot"}}
	case "system.check":
		return intent, []ToolCall{{Name: "system.snapshot"}, {Name: "process.inspect"}}
	}
	return intent, nil
}

func (r *ToolRegistry) Execute(parent context.Context, call ToolCall) ToolStep {
	started := time.Now()
	step := ToolStep{Name: call.Name, Status: "error", ReadOnly: true, StartedAt: started, Evidence: []string{}}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()

	switch call.Name {
	case "system.snapshot":
		step.Title = "Состояние роутера"
		step.Summary, step.Evidence, step.Status = r.executeSource(ctx, r.sources.SystemSnapshot, "Состояние роутера получено")
	case "system.services":
		step.Title = "Сервисы Entware"
		step.Summary, step.Evidence, step.Status = r.executeSource(ctx, r.sources.SystemServices, "Список сервисов получен")
	case "system.ports":
		step.Title = "Открытые порты"
		step.Summary, step.Evidence, step.Status = r.executeSource(ctx, r.sources.SystemPorts, "Список портов получен")
	case "system.packages":
		step.Title = "Пакеты Entware"
		if r.sources.SystemPackages == nil {
			step.Summary = "Источник пакетов не настроен"
			break
		}
		value, err := r.sources.SystemPackages(ctx, strings.TrimSpace(call.Arguments["kind"]), strings.TrimSpace(call.Arguments["query"]))
		step.Summary, step.Evidence, step.Status = sourceResult(value, err, "Данные пакетов получены")
	case "system.files.list":
		step.Title = "Файлы Entware"
		if r.sources.SystemFilesList == nil {
			step.Summary = "Источник файлов Entware не настроен"
			break
		}
		value, err := r.sources.SystemFilesList(ctx, strings.TrimSpace(call.Arguments["path"]))
		step.Summary, step.Evidence, step.Status = sourceResult(value, err, "Содержимое каталога получено")
	case "system.file.read_safe":
		step.Title = "Безопасное чтение файла"
		if r.sources.SystemFileRead == nil {
			step.Summary = "Источник файлов Entware не настроен"
			break
		}
		value, err := r.sources.SystemFileRead(ctx, strings.TrimSpace(call.Arguments["path"]))
		step.Summary, step.Evidence, step.Status = sourceResult(value, err, "Текст файла получен с маскированием секретов")
	case "system.diagnose_command":
		step.Title = "Диагностическая команда ОС"
		step.Summary, step.Evidence, step.Status = r.executeDiagnoseCommand(ctx, call.Arguments["command"], call.Arguments["timeoutSeconds"])
	case "diagnostics.full":
		step.Title = "Полная диагностика AWG Manager"
		step.Summary, step.Evidence, step.Status = r.executeSource(ctx, r.sources.FullDiagnostics, "Полная диагностика завершена")
	case "domain.inspect":
		step.Title = "Проверка домена и маршрута"
		step.Summary, step.Evidence, step.Status = r.inspectDomain(ctx, call.Arguments["domain"])
	case "routing.snapshot":
		step.Title = "Снимок маршрутизации"
		step.Summary, step.Evidence, step.Status = r.routingSnapshot(ctx)
	case "dns.inspect":
		step.Title = "Проверка DNS"
		step.Summary, step.Evidence, step.Status = r.inspectDNS(ctx, call.Arguments["domain"])
	case "process.inspect":
		step.Title = "Процессы сетевых движков"
		step.Summary, step.Evidence, step.Status = r.inspectProcesses(ctx)
	case "engines.status":
		step.Title = "Состояние движков"
		step.Summary, step.Evidence, step.Status = r.executeSource(ctx, r.sources.EngineStatus, "Получено состояние движков")
	case "tunnels.list":
		step.Title = "Список туннелей"
		step.Summary, step.Evidence, step.Status = r.executeSource(ctx, r.sources.Tunnels, "Получена сводка туннелей")
	case "tunnels.status":
		step.Title = "Состояние туннеля"
		if r.sources.TunnelStatus == nil {
			step.Summary = "Источник состояния туннелей не настроен"
			break
		}
		value, err := r.sources.TunnelStatus(ctx, strings.TrimSpace(call.Arguments["id"]))
		step.Summary, step.Evidence, step.Status = sourceResult(value, err, "Получено состояние туннеля")
	case "subscriptions.status":
		step.Title = "Состояние подписок"
		step.Summary, step.Evidence, step.Status = r.executeSource(ctx, r.sources.Subscriptions, "Получена сводка подписок")
	case "connections.search":
		step.Title = "Поиск соединений"
		limit := parseToolLimit(call.Arguments["limit"], 20, 50)
		if r.sources.Connections == nil {
			step.Summary = "Источник соединений не настроен"
			break
		}
		value, err := r.sources.Connections(ctx, strings.TrimSpace(call.Arguments["search"]), limit)
		step.Summary, step.Evidence, step.Status = sourceResult(value, err, "Получена выборка соединений")
	case "routing.explain_client":
		step.Title = "Объяснение маршрута клиента"
		if r.sources.ExplainClient == nil {
			step.Summary = "Источник маршрутизации клиентов не настроен"
			break
		}
		value, err := r.sources.ExplainClient(ctx, strings.TrimSpace(call.Arguments["client"]))
		step.Summary, step.Evidence, step.Status = sourceResult(value, err, "Собраны данные маршрута клиента")
	case "logs.tail":
		step.Title = "Последние записи журнала"
		if r.sources.Logs == nil {
			step.Summary = "Источник журналов не настроен"
			break
		}
		limit := parseToolLimit(call.Arguments["limit"], 30, 100)
		value, err := r.sources.Logs(ctx, strings.TrimSpace(call.Arguments["bucket"]), strings.TrimSpace(call.Arguments["group"]), strings.TrimSpace(call.Arguments["level"]), limit)
		step.Summary, step.Evidence, step.Status = sourceResult(value, err, "Получены последние записи журнала")
	case "proxy.groups":
		step.Title = "Состояние proxy-групп"
		if r.sources.ProxyGroups == nil {
			step.Summary = "Источник proxy-групп не настроен"
			break
		}
		value, err := r.sources.ProxyGroups(ctx, strings.TrimSpace(call.Arguments["engine"]))
		step.Summary, step.Evidence, step.Status = sourceResult(value, err, "Получено состояние proxy-групп")
	case "routing.explain_rule":
		step.Title = "Объяснение правила назначения"
		if r.sources.InspectRule == nil {
			step.Summary = "Инспектор правил не настроен"
			break
		}
		port := parseToolLimit(call.Arguments["port"], 0, 65535)
		value, err := r.sources.InspectRule(ctx, strings.TrimSpace(call.Arguments["destination"]), port, strings.TrimSpace(call.Arguments["protocol"]))
		step.Summary, step.Evidence, step.Status = sourceResult(value, err, "Правила назначения проверены")
	case "outbound.test":
		step.Title = "Проверка outbound"
		if r.sources.TestOutbound == nil {
			step.Summary = "Проверка outbound не настроена"
			break
		}
		value, err := r.sources.TestOutbound(ctx, strings.TrimSpace(call.Arguments["engine"]), strings.TrimSpace(call.Arguments["name"]))
		step.Summary, step.Evidence, step.Status = sourceResult(value, err, "Outbound отвечает")
	case "dns.explain":
		step.Title = "Объяснение DNS-цепочки"
		if r.sources.ExplainDNS == nil {
			step.Summary = "DNS-инспектор не настроен"
			break
		}
		value, err := r.sources.ExplainDNS(ctx, strings.TrimSpace(call.Arguments["domain"]), strings.TrimSpace(call.Arguments["sourceIp"]), strings.TrimSpace(call.Arguments["queryType"]))
		step.Summary, step.Evidence, step.Status = sourceResult(value, err, "DNS-цепочка проверена")
	case "connections.explain":
		step.Title = "Объяснение live-соединения"
		if r.sources.ExplainConn == nil {
			step.Summary = "Инспектор соединений не настроен"
			break
		}
		sourcePort := parseToolLimit(call.Arguments["sourcePort"], 0, 65535)
		destinationPort := parseToolLimit(call.Arguments["destinationPort"], 0, 65535)
		value, err := r.sources.ExplainConn(ctx, strings.TrimSpace(call.Arguments["source"]), sourcePort, strings.TrimSpace(call.Arguments["destination"]), destinationPort, strings.TrimSpace(call.Arguments["protocol"]))
		step.Summary, step.Evidence, step.Status = sourceResult(value, err, "Live-соединение и правило сопоставлены")
	case "remediation.propose":
		step.Title = "Предложение исправления"
		proposal := validatedRemediationProposal(call.Arguments["action"], call.Arguments["target"])
		if proposal == nil {
			step.Summary = "Действие или цель не разрешены"
			break
		}
		step.Summary, step.Evidence, step.Status = sourceResult(proposal, nil, "Исправление подготовлено и ожидает подтверждения")
	case "memory.learn_fact":
		step.Title = "Запоминание факта"
		if r.memory == nil {
			step.Summary = "Долговременная память ассистента не подключена"
			break
		}
		category := strings.TrimSpace(call.Arguments["category"])
		fact := strings.TrimSpace(call.Arguments["fact"])
		if fact == "" {
			fact = strings.TrimSpace(call.Arguments["content"])
		}
		source := strings.TrimSpace(call.Arguments["source"])
		if source == "" {
			source = "ai_dialog"
		}
		if fact == "" {
			step.Summary = "Параметр fact или content обязателен"
			break
		}
		item := r.memory.AddFact(category, fact, source)
		step.Summary, step.Evidence, step.Status = sourceResult(item, nil, fmt.Sprintf("Факт успешно сохранён: [%s] %s", item.Category, item.Content))
	case "memory.save_playbook":
		step.Title = "Сохранение сценария устранения"
		if r.memory == nil {
			step.Summary = "Долговременная память ассистента не подключена"
			break
		}
		symptom := strings.TrimSpace(call.Arguments["symptom"])
		diagnosis := strings.TrimSpace(call.Arguments["diagnosis"])
		action := strings.TrimSpace(call.Arguments["remediationAction"])
		target := strings.TrimSpace(call.Arguments["remediationTarget"])
		category := strings.TrimSpace(call.Arguments["category"])
		title := strings.TrimSpace(call.Arguments["title"])
		if title == "" {
			title = symptom
		}
		if category == "" {
			category = "routing"
		}
		if symptom == "" || action == "" {
			step.Summary = "Параметры symptom и remediationAction обязательны"
			break
		}
		pb := r.memory.AddOrUpdatePlaybook(LearnedPlaybook{
			Category:    category,
			Title:       title,
			Trigger:     symptom,
			Diagnosis:   diagnosis,
			Action:      action,
			Target:      target,
			LearnedFrom: "ai_dialog",
		})
		step.Summary, step.Evidence, step.Status = sourceResult(pb, nil, fmt.Sprintf("Playbook успешно сохранён: %s -> %s(%s)", pb.Trigger, pb.Action, pb.Target))
	case "memory.list_facts":
		step.Title = "База знаний ассистента"
		if r.memory == nil {
			step.Summary = "Долговременная память ассистента не подключена"
			break
		}
		facts := r.memory.ListFacts("")
		playbooks := r.memory.ListPlaybooks()
		step.Summary, step.Evidence, step.Status = sourceResult(map[string]any{
			"facts":     facts,
			"playbooks": playbooks,
		}, nil, fmt.Sprintf("Загружено %d фактов и %d playbooks", len(facts), len(playbooks)))
	default:
		step.Title = "Неизвестный инструмент"
		step.Summary = "Инструмент не зарегистрирован"
	}
	step.Duration = time.Since(started)
	step.DurationMS = step.Duration.Milliseconds()
	return step
}

func (r *ToolRegistry) executeSource(ctx context.Context, source func(context.Context) (any, error), success string) (string, []string, string) {
	if source == nil {
		return "Источник данных не настроен", nil, "error"
	}
	value, err := source(ctx)
	return sourceResult(value, err, success)
}

func sourceResult(value any, err error, success string) (string, []string, string) {
	if err != nil {
		return "Не удалось получить данные AWG Manager", []string{sanitizeToolEvidence(err.Error())}, "error"
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "Не удалось сериализовать данные AWG Manager", []string{err.Error()}, "error"
	}
	return success, []string{truncateToolEvidence(string(raw), 12000)}, "passed"
}

func parseToolLimit(raw string, fallback, maximum int) int {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value <= 0 {
		return fallback
	}
	if value > maximum {
		return maximum
	}
	return value
}

func sanitizeToolEvidence(value string) string {
	value = strings.ReplaceAll(value, "\r", " ")
	return strings.ReplaceAll(value, "\n", " ")
}

func truncateToolEvidence(value string, limit int) string {
	value = sanitizeToolEvidence(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}

func (r *ToolRegistry) routingSnapshot(ctx context.Context) (string, []string, string) {
	checks := []struct {
		label string
		args  []string
	}{
		{label: "ip rule", args: []string{"rule", "show"}},
		{label: "main routes", args: []string{"route", "show", "table", "main"}},
		{label: "all policy routes", args: []string{"route", "show", "table", "all"}},
	}
	evidence := make([]string, 0, 24)
	status := "passed"
	for _, check := range checks {
		result, err := r.run(ctx, "/opt/sbin/ip", check.args...)
		if err != nil {
			status = "warning"
			evidence = append(evidence, check.label+": "+err.Error())
			continue
		}
		lines := limitedNonEmptyLines(result.Stdout, 8)
		if len(lines) == 0 {
			status = "warning"
			evidence = append(evidence, check.label+": пусто")
			continue
		}
		for _, line := range lines {
			evidence = append(evidence, check.label+": "+line)
		}
	}
	return "Собраны правила и таблицы маршрутизации", evidence, status
}

func (r *ToolRegistry) inspectDNS(ctx context.Context, domain string) (string, []string, string) {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		domain = "connectivitycheck.gstatic.com"
	}
	if !validDomain(domain) {
		return "Некорректное имя домена", nil, "error"
	}
	addresses, err := r.resolver.LookupHost(ctx, domain)
	if err != nil {
		return "Системный DNS не разрешил домен", []string{domain + ": " + err.Error()}, "error"
	}
	sort.Strings(addresses)
	addresses = uniqueStrings(addresses)
	if len(addresses) > 6 {
		addresses = addresses[:6]
	}
	return "Системный DNS отвечает", []string{fmt.Sprintf("%s -> %s", domain, strings.Join(addresses, ", "))}, "passed"
}

func (r *ToolRegistry) inspectProcesses(ctx context.Context) (string, []string, string) {
	result, err := r.run(ctx, "/bin/ps", "w")
	if err != nil {
		return "Не удалось получить список процессов", []string{err.Error()}, "warning"
	}
	needles := []string{"awg-manager", "sing-box", "singbox", "mihomo", "wdtt", "unbound", "dnsmasq"}
	var evidence []string
	for _, line := range limitedNonEmptyLines(result.Stdout, 200) {
		lower := strings.ToLower(line)
		for _, needle := range needles {
			if strings.Contains(lower, needle) {
				evidence = append(evidence, line)
				break
			}
		}
		if len(evidence) >= 16 {
			break
		}
	}
	if len(evidence) == 0 {
		return "Сетевые процессы в выводе ps не найдены", nil, "warning"
	}
	return fmt.Sprintf("Найдено сетевых процессов: %d", len(evidence)), evidence, "passed"
}

func limitedNonEmptyLines(value string, limit int) []string {
	lines := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	result := make([]string, 0, min(limit, len(lines)))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > 240 {
			line = line[:240]
		}
		result = append(result, line)
		if len(result) >= limit {
			break
		}
	}
	return result
}

func (r *ToolRegistry) inspectDomain(ctx context.Context, domain string) (string, []string, string) {
	if !validDomain(domain) {
		return "Некорректное имя домена", nil, "error"
	}
	addresses, err := r.resolver.LookupHost(ctx, domain)
	if err != nil {
		return "DNS не разрешил домен", []string{fmt.Sprintf("%s: %v", domain, err)}, "error"
	}
	sort.Strings(addresses)
	addresses = uniqueStrings(addresses)
	if len(addresses) > 4 {
		addresses = addresses[:4]
	}
	evidence := []string{fmt.Sprintf("%s -> %s", domain, strings.Join(addresses, ", "))}
	status := "passed"
	for _, address := range addresses {
		result, routeErr := r.run(ctx, "/opt/sbin/ip", "route", "get", address)
		if routeErr != nil {
			status = "warning"
			evidence = append(evidence, fmt.Sprintf("route %s: %v", address, routeErr))
			continue
		}
		route := strings.TrimSpace(result.Stdout)
		if route == "" {
			status = "warning"
			route = "маршрут не найден"
		}
		route = firstLine(route)
		if strings.HasPrefix(strings.ToLower(route), "unreachable") {
			status = "warning"
		}
		evidence = append(evidence, fmt.Sprintf("route %s: %s", address, route))
	}
	return fmt.Sprintf("Домен %s разрешён. Проверено системных IP-маршрутов: %d. Эта проверка не подтверждает прохождение трафика через TProxy или прокси-движок", domain, len(addresses)), evidence, status
}

func validDomain(value string) bool {
	match := domainPattern.FindStringSubmatch(value)
	return len(match) > 1 && strings.EqualFold(match[1], value) && len(value) <= 253
}

func uniqueStrings(values []string) []string {
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

func firstLine(value string) string {
	if before, _, ok := strings.Cut(value, "\n"); ok {
		return before
	}
	return value
}

func (r *ToolRegistry) executeDiagnoseCommand(ctx context.Context, cmdStr, timeoutStr string) (string, []string, string) {
	cmdStr = strings.TrimSpace(cmdStr)
	if cmdStr == "" {
		return "Ошибка: команда не указана", []string{"Команда не указана"}, "error"
	}

	if err := ValidateDiagnosticCommand(cmdStr); err != nil {
		return "Команда отклонена защитным фильтром", []string{err.Error()}, "error"
	}

	timeoutSec := 8
	if t, err := strconv.Atoi(strings.TrimSpace(timeoutStr)); err == nil && t >= 1 && t <= 15 {
		timeoutSec = t
	}

	cmdCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	result, err := r.run(cmdCtx, "/bin/sh", "-c", cmdStr)
	if err != nil && result == nil {
		return "Не удалось выполнить диагностическую команду", []string{err.Error()}, "error"
	}

	stdout := ""
	stderr := ""
	exitCode := 0
	if result != nil {
		stdout = result.Stdout
		stderr = result.Stderr
		exitCode = result.ExitCode
	}

	sanitized := SanitizeDiagnosticOutput(stdout, stderr, 12000)
	evidence := []string{sanitized}
	if sanitized == "" {
		evidence = []string{fmt.Sprintf("(команда выполнена без вывода, код завершения: %d)", exitCode)}
	}

	status := "passed"
	summary := fmt.Sprintf("Команда выполнена (код: %d)", exitCode)
	if exitCode != 0 {
		status = "warning"
		summary = fmt.Sprintf("Команда завершилась с кодом %d", exitCode)
	}

	return summary, evidence, status
}
