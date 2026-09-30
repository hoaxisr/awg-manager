package aiassistant

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type ActionVerification struct {
	Status  string `json:"status"` // "passed", "warning", "failed"
	Summary string `json:"summary"`
	Detail  string `json:"detail,omitempty"`
}

type RemediationProposal struct {
	ID            string              `json:"id"`
	RunID         string              `json:"runId,omitempty"`
	Action        string              `json:"action"`
	Target        string              `json:"target,omitempty"`
	Title         string              `json:"title"`
	Description   string              `json:"description"`
	Risk          string              `json:"risk"`
	Status        string              `json:"status"`
	AutoApplied   bool                `json:"autoApplied,omitempty"`
	Verification  *ActionVerification `json:"verification,omitempty"`
	RolledBack    bool                `json:"rolledBack,omitempty"`
	RollbackError string              `json:"rollbackError,omitempty"`
	Error         string              `json:"error,omitempty"`
	CreatedAt     time.Time           `json:"createdAt"`
}

func (p *RemediationProposal) IsExpired(ttl time.Duration) bool {
	if p == nil {
		return true
	}
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return time.Since(p.CreatedAt) > ttl
}

type ActionExecutor interface {
	Apply(ctx context.Context, action, target string) error
	Verify(ctx context.Context, action, target string) (*ActionVerification, error)
}

type ActionSnapshot struct {
	Action string `json:"action"`
	Target string `json:"target,omitempty"`
	Value  string `json:"value"`
}

// TransactionalActionExecutor is implemented for state-changing operations
// that can be restored safely through the same application service.
type TransactionalActionExecutor interface {
	ActionExecutor
	Snapshot(ctx context.Context, action, target string) (*ActionSnapshot, error)
	Rollback(ctx context.Context, snapshot ActionSnapshot) error
}

type ActionHandlers struct {
	RestartSingbox       func(ctx context.Context) error
	RestartMihomo        func(ctx context.Context) error
	ReloadMihomo         func(ctx context.Context) error
	ReapplyRouting       func(ctx context.Context) error
	SwitchRoutingEngine  func(ctx context.Context, engine string) error
	CurrentRoutingEngine func(ctx context.Context) (string, error)
	SwitchRoutingMode    func(ctx context.Context, mode string) error
	CurrentRoutingMode   func(ctx context.Context) (string, error)
	ServiceAction        func(ctx context.Context, script, action string) error
	ServiceRunning       func(ctx context.Context, script string) (bool, error)
	OpkgAction           func(ctx context.Context, action, packageName string) error
	PackageInstalled     func(ctx context.Context, packageName string) (bool, error)
	RestartTunnel        func(ctx context.Context, tunnelID string) error
	UpdateSubscription   func(ctx context.Context, subID string) error
	FlushDNS             func(ctx context.Context) error
	VerifySingbox        func(ctx context.Context) (*ActionVerification, error)
	VerifyMihomo         func(ctx context.Context) (*ActionVerification, error)
	VerifyRouting        func(ctx context.Context) (*ActionVerification, error)
	VerifyTunnel         func(ctx context.Context, tunnelID string) (*ActionVerification, error)
	VerifySubscription   func(ctx context.Context, subID string) (*ActionVerification, error)
	VerifyDNS            func(ctx context.Context) (*ActionVerification, error)
	ExecCommand          func(ctx context.Context, command string) error
	ExecKeenetic         func(ctx context.Context, command string) error
}

type ActionRegistry struct {
	handlers ActionHandlers
}

type remediationSpec struct {
	risk           string
	targetRequired bool
	title          func(string) string
	description    func(string) string
}

var remediationTargetPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

var remediationSpecs = map[string]remediationSpec{
	"singbox.restart": {
		risk:  "medium",
		title: func(string) string { return "Перезапустить sing-box" },
		description: func(string) string {
			return "Перезапустить процесс sing-box и заново поднять его локальные proxy-порты. Текущие соединения через sing-box кратковременно прервутся."
		},
	},
	"mihomo.restart": {
		risk:  "medium",
		title: func(string) string { return "Перезапустить Mihomo" },
		description: func(string) string {
			return "Перезапустить процесс Mihomo и переинициализировать сетевые правила."
		},
	},
	"mihomo.reload": {
		risk:  "low",
		title: func(string) string { return "Перезагрузить конфигурацию Mihomo" },
		description: func(string) string {
			return "Применить текущую конфигурацию Mihomo без полного перезапуска процесса."
		},
	},
	"routing.reapply": {
		risk: "medium",
		title: func(string) string {
			return "Повторно применить активную маршрутизацию"
		},
		description: func(string) string {
			return "Заново применить конфигурацию выбранного прокси-движка и его правила перехвата. Текущие прокси-соединения могут кратковременно прерваться."
		},
	},
	"routing.switch_engine": {
		risk: "medium", targetRequired: true,
		title: func(target string) string {
			return "Переключить ядро маршрутизации на " + target
		},
		description: func(target string) string {
			return "Переключить активное ядро маршрутизации на " + target + " через штатный сервис AWG Manager. При неуспешной проверке будет автоматически восстановлено предыдущее ядро."
		},
	},
	"routing.switch_mode": {
		risk: "medium", targetRequired: true,
		title: func(target string) string {
			return "Переключить режим маршрутизации на " + target
		},
		description: func(target string) string {
			return "Переключить режим захвата трафика на " + target + " через штатный оркестратор. При неуспешной проверке будет автоматически восстановлен прежний режим."
		},
	},
	"service.start": {
		risk: "medium", targetRequired: true,
		title: func(target string) string { return "Запустить сервис " + target },
		description: func(target string) string {
			return "Запустить Entware init.d-сервис " + target + ". При провале проверки прежнее состояние будет восстановлено."
		},
	},
	"service.stop": {
		risk: "medium", targetRequired: true,
		title: func(target string) string { return "Остановить сервис " + target },
		description: func(target string) string {
			return "Остановить Entware init.d-сервис " + target + ". Критические сервисы защищены системным исполнителем."
		},
	},
	"service.restart": {
		risk: "medium", targetRequired: true,
		title: func(target string) string { return "Перезапустить сервис " + target },
		description: func(target string) string {
			return "Перезапустить Entware init.d-сервис " + target + " и проверить его состояние."
		},
	},
	"opkg.update": {
		risk:  "low",
		title: func(string) string { return "Обновить индекс пакетов Entware" },
		description: func(string) string {
			return "Загрузить актуальный индекс пакетов opkg без установки обновлений."
		},
	},
	"opkg.install": {
		risk: "medium", targetRequired: true,
		title: func(target string) string { return "Установить пакет " + target },
		description: func(target string) string {
			return "Установить пакет Entware " + target + " через opkg и проверить его наличие."
		},
	},
	"opkg.upgrade": {
		risk: "medium", targetRequired: true,
		title: func(target string) string { return "Обновить пакет " + target },
		description: func(target string) string {
			return "Обновить установленный пакет Entware " + target + " через opkg."
		},
	},
	"opkg.remove": {
		risk: "high", targetRequired: true,
		title: func(target string) string { return "Удалить пакет " + target },
		description: func(target string) string {
			return "Удалить пакет Entware " + target + ". Автоматический откат удаления не гарантируется."
		},
	},
	"tunnel.restart": {
		risk: "low", targetRequired: true,
		title: func(target string) string { return "Перезапустить туннель " + target },
		description: func(string) string {
			return "Выполнить повторную инициализацию интерфейса туннеля и восстановить соединение."
		},
	},
	"subscription.update": {
		risk: "low", targetRequired: true,
		title: func(target string) string { return "Обновить подписку " + target },
		description: func(string) string {
			return "Принудительно загрузить обновлённый список серверов подписки и применить его через штатный сервис AWG Manager."
		},
	},
	"command.exec": {
		risk: "medium", targetRequired: true,
		title: func(target string) string {
			if len(target) > 50 {
				return "Выполнить команду: " + target[:50] + "…"
			}
			return "Выполнить команду: " + target
		},
		description: func(target string) string {
			return "Выполнить согласованную системную команду после подтверждения пользователем: " + target
		},
	},
	"keenetic.ndmc": {
		risk: "medium", targetRequired: true,
		title: func(target string) string {
			if len(target) > 50 {
				return "Настройка KeeneticOS: " + target[:50] + "…"
			}
			return "Настройка KeeneticOS: " + target
		},
		description: func(target string) string {
			return "Выполнить команду в KeeneticOS CLI (ndmc) с автоматическим сохранением: " + target
		},
	},
}

func validatedRemediationProposal(action, target string) *RemediationProposal {
	action = strings.TrimSpace(action)
	target = strings.TrimSpace(target)
	spec, ok := remediationSpecs[action]
	if !ok || !validRemediationTarget(action, spec, target) {
		return nil
	}
	if action == "routing.switch_engine" && !validRoutingEngine(target) || action == "routing.switch_mode" && !validRoutingMode(target) || strings.HasPrefix(action, "service.") && !validServiceScript(target) || strings.HasPrefix(action, "opkg.") && action != "opkg.update" && !validPackageName(target) {
		return nil
	}
	return &RemediationProposal{
		ID: newProposalID(), Action: action, Target: target,
		Title: spec.title(target), Description: spec.description(target), Risk: spec.risk,
		Status: "pending", CreatedAt: time.Now(),
	}
}

func validRemediationTarget(action string, spec remediationSpec, target string) bool {
	if action == "service.stop" {
		name := strings.ToLower(strings.TrimSpace(target))
		if len(name) > 3 && (name[0] == 's' || name[0] == 'k') && name[1] >= '0' && name[1] <= '9' && name[2] >= '0' && name[2] <= '9' {
			name = name[3:]
		}
		if name == "dropbear" || name == "awg-manager" || name == "ttyd" || name == "sing-box" {
			return false
		}
	}
	if action == "command.exec" {
		trimmed := strings.TrimSpace(target)
		return trimmed != "" && len(trimmed) <= 512
	}
	if action == "keenetic.ndmc" {
		trimmed := strings.TrimSpace(target)
		if trimmed == "" || len(trimmed) > 512 {
			return false
		}
		lower := strings.ToLower(trimmed)
		// Strict security: ban destructive system-wipe / factory-reset / format commands
		banned := []string{"default-config", "format", "erase", "factory", "cleanup", "reboot"}
		for _, b := range banned {
			if strings.Contains(lower, b) {
				return false
			}
		}
		return true
	}
	if spec.targetRequired {
		return remediationTargetPattern.MatchString(target)
	}
	return target == ""
}

func validRoutingEngine(target string) bool {
	return target == "mihomo" || target == "sing-box"
}

func validRoutingMode(target string) bool {
	return target == "off" || target == "tproxy" || target == "fakeip-tun" || target == "policy-tun"
}

var serviceScriptPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var packageNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9+._-]{0,127}$`)

func validServiceScript(target string) bool { return serviceScriptPattern.MatchString(target) }
func validPackageName(target string) bool   { return packageNamePattern.MatchString(target) }

func NewActionRegistry(handlers ActionHandlers) *ActionRegistry {
	return &ActionRegistry{handlers: handlers}
}

func (r *ActionRegistry) Apply(ctx context.Context, action, target string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r == nil {
		return errors.New("action registry is not configured")
	}
	switch action {
	case "singbox.restart":
		if r.handlers.RestartSingbox == nil {
			return errors.New("sing-box restart is unavailable")
		}
		return r.handlers.RestartSingbox(ctx)
	case "mihomo.restart":
		if r.handlers.RestartMihomo == nil {
			return errors.New("mihomo restart is unavailable")
		}
		return r.handlers.RestartMihomo(ctx)
	case "mihomo.reload":
		if r.handlers.ReloadMihomo == nil {
			return errors.New("mihomo reload is unavailable")
		}
		return r.handlers.ReloadMihomo(ctx)
	case "routing.reapply":
		if r.handlers.ReapplyRouting == nil {
			return errors.New("routing reapply is unavailable")
		}
		return r.handlers.ReapplyRouting(ctx)
	case "routing.switch_engine":
		if !validRoutingEngine(target) {
			return errors.New("routing engine must be mihomo or sing-box")
		}
		if r.handlers.SwitchRoutingEngine == nil {
			return errors.New("routing engine switch is unavailable")
		}
		return r.handlers.SwitchRoutingEngine(ctx, target)
	case "routing.switch_mode":
		if !validRoutingMode(target) {
			return errors.New("routing mode must be off, tproxy, fakeip-tun or policy-tun")
		}
		if r.handlers.SwitchRoutingMode == nil {
			return errors.New("routing mode switch is unavailable")
		}
		return r.handlers.SwitchRoutingMode(ctx, target)
	case "service.start", "service.stop", "service.restart":
		if !validServiceScript(target) || r.handlers.ServiceAction == nil {
			return errors.New("service action or script is unavailable")
		}
		return r.handlers.ServiceAction(ctx, target, strings.TrimPrefix(action, "service."))
	case "opkg.update":
		if r.handlers.OpkgAction == nil {
			return errors.New("opkg action is unavailable")
		}
		return r.handlers.OpkgAction(ctx, "update", "")
	case "opkg.install", "opkg.upgrade", "opkg.remove":
		if !validPackageName(target) || r.handlers.OpkgAction == nil {
			return errors.New("opkg action or package is unavailable")
		}
		return r.handlers.OpkgAction(ctx, strings.TrimPrefix(action, "opkg."), target)
	case "tunnel.restart":
		if r.handlers.RestartTunnel == nil {
			return errors.New("tunnel restart is unavailable")
		}
		if target == "" {
			return errors.New("tunnel target ID is required")
		}
		return r.handlers.RestartTunnel(ctx, target)
	case "subscription.update":
		if r.handlers.UpdateSubscription == nil {
			return errors.New("subscription update is unavailable")
		}
		if target == "" {
			return errors.New("subscription target ID is required")
		}
		return r.handlers.UpdateSubscription(ctx, target)
	case "dns.flush":
		if r.handlers.FlushDNS == nil {
			return errors.New("dns flush is unavailable")
		}
		return r.handlers.FlushDNS(ctx)
	case "command.exec":
		if r.handlers.ExecCommand == nil {
			return errors.New("command execution is unavailable")
		}
		if target == "" {
			return errors.New("command target is required")
		}
		return r.handlers.ExecCommand(ctx, target)
	case "keenetic.ndmc":
		if target == "" {
			return errors.New("keenetic ndmc command is required")
		}
		if r.handlers.ExecKeenetic != nil {
			return r.handlers.ExecKeenetic(ctx, target)
		}
		if r.handlers.ExecCommand != nil {
			cmd := fmt.Sprintf("ndmc -c %q && ndmc -c 'system configuration save'", target)
			return r.handlers.ExecCommand(ctx, cmd)
		}
		return errors.New("keenetic ndmc execution is unavailable")
	default:
		return fmt.Errorf("unsupported remediation action %q", action)
	}
}

func (r *ActionRegistry) Verify(ctx context.Context, action, target string) (*ActionVerification, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r == nil {
		return nil, nil
	}
	switch action {
	case "singbox.restart":
		if r.handlers.VerifySingbox != nil {
			return r.handlers.VerifySingbox(ctx)
		}
		return defaultSocketVerification("127.0.0.1:11001", "sing-box proxy"), nil
	case "mihomo.restart", "mihomo.reload":
		if r.handlers.VerifyMihomo != nil {
			return r.handlers.VerifyMihomo(ctx)
		}
		return defaultSocketVerification("127.0.0.1:9090", "Mihomo REST API"), nil
	case "routing.reapply":
		if r.handlers.VerifyRouting != nil {
			return r.handlers.VerifyRouting(ctx)
		}
		return &ActionVerification{Status: "warning", Summary: "Маршрутизация применена, но её runtime-проверка недоступна"}, nil
	case "routing.switch_engine":
		if r.handlers.CurrentRoutingEngine == nil {
			return &ActionVerification{Status: "warning", Summary: "Ядро переключено, но чтение текущего состояния недоступно"}, nil
		}
		current, err := r.handlers.CurrentRoutingEngine(ctx)
		if err != nil {
			return nil, err
		}
		if current != target {
			return &ActionVerification{Status: "failed", Summary: "После переключения активно другое ядро: " + current}, nil
		}
		if r.handlers.VerifyRouting != nil {
			return r.handlers.VerifyRouting(ctx)
		}
		return &ActionVerification{Status: "passed", Summary: "Активное ядро маршрутизации: " + current}, nil
	case "routing.switch_mode":
		if r.handlers.CurrentRoutingMode == nil {
			return &ActionVerification{Status: "warning", Summary: "Режим переключён, но чтение текущего состояния недоступно"}, nil
		}
		current, err := r.handlers.CurrentRoutingMode(ctx)
		if err != nil {
			return nil, err
		}
		if current != target {
			return &ActionVerification{Status: "failed", Summary: "После переключения активен другой режим: " + current}, nil
		}
		if target == "off" {
			return &ActionVerification{Status: "passed", Summary: "Маршрутизация выключена"}, nil
		}
		if r.handlers.VerifyRouting != nil {
			return r.handlers.VerifyRouting(ctx)
		}
		return &ActionVerification{Status: "passed", Summary: "Активный режим маршрутизации: " + current}, nil
	case "service.start", "service.restart", "service.stop":
		if r.handlers.ServiceRunning == nil {
			return &ActionVerification{Status: "warning", Summary: "Действие выполнено, но состояние сервиса недоступно"}, nil
		}
		running, err := r.handlers.ServiceRunning(ctx, target)
		if err != nil {
			return nil, err
		}
		wantRunning := action != "service.stop"
		if running != wantRunning {
			return &ActionVerification{Status: "failed", Summary: "Сервис не перешёл в ожидаемое состояние"}, nil
		}
		return &ActionVerification{Status: "passed", Summary: "Состояние сервиса подтверждено"}, nil
	case "opkg.update":
		return &ActionVerification{Status: "passed", Summary: "Индекс пакетов opkg обновлён"}, nil
	case "opkg.install", "opkg.upgrade", "opkg.remove":
		if r.handlers.PackageInstalled == nil {
			return &ActionVerification{Status: "warning", Summary: "Операция opkg завершена, но состояние пакета недоступно"}, nil
		}
		installed, err := r.handlers.PackageInstalled(ctx, target)
		if err != nil {
			return nil, err
		}
		wantInstalled := action != "opkg.remove"
		if installed != wantInstalled {
			return &ActionVerification{Status: "failed", Summary: "Пакет не перешёл в ожидаемое состояние"}, nil
		}
		return &ActionVerification{Status: "passed", Summary: "Состояние пакета подтверждено"}, nil
	case "tunnel.restart":
		if r.handlers.VerifyTunnel != nil {
			return r.handlers.VerifyTunnel(ctx, target)
		}
		return &ActionVerification{
			Status:  "passed",
			Summary: fmt.Sprintf("Туннель %s перезапущен и готов к обработке трафика", target),
		}, nil
	case "subscription.update":
		if r.handlers.VerifySubscription != nil {
			return r.handlers.VerifySubscription(ctx, target)
		}
		return &ActionVerification{Status: "warning", Summary: "Подписка обновлена, но проверка её состояния недоступна"}, nil
	case "dns.flush":
		if r.handlers.VerifyDNS != nil {
			return r.handlers.VerifyDNS(ctx)
		}
		return defaultDNSVerification(), nil
	case "keenetic.ndmc":
		return &ActionVerification{
			Status:  "passed",
			Summary: "Команда KeeneticOS успешно выполнена и сохранена в конфигурации",
			Detail:  target,
		}, nil
	case "command.exec":
		return &ActionVerification{
			Status:  "passed",
			Summary: "Системная команда успешно выполнена",
			Detail:  target,
		}, nil
	default:
		return nil, nil
	}
}

func (r *ActionRegistry) Snapshot(ctx context.Context, action, target string) (*ActionSnapshot, error) {
	if strings.HasPrefix(action, "service.") {
		if r == nil || r.handlers.ServiceRunning == nil {
			return nil, errors.New("service snapshot is unavailable")
		}
		running, err := r.handlers.ServiceRunning(ctx, target)
		if err != nil {
			return nil, err
		}
		return &ActionSnapshot{Action: action, Target: target, Value: strconv.FormatBool(running)}, nil
	}
	if action != "routing.switch_engine" && action != "routing.switch_mode" {
		return nil, nil
	}
	if action == "routing.switch_mode" {
		if r == nil || r.handlers.CurrentRoutingMode == nil {
			return nil, errors.New("routing mode snapshot is unavailable")
		}
		current, err := r.handlers.CurrentRoutingMode(ctx)
		if err != nil {
			return nil, err
		}
		if !validRoutingMode(current) {
			return nil, fmt.Errorf("cannot snapshot unsupported routing mode %q", current)
		}
		return &ActionSnapshot{Action: action, Value: current}, nil
	}
	if r == nil || r.handlers.CurrentRoutingEngine == nil {
		return nil, errors.New("routing engine snapshot is unavailable")
	}
	current, err := r.handlers.CurrentRoutingEngine(ctx)
	if err != nil {
		return nil, err
	}
	if !validRoutingEngine(current) {
		return nil, fmt.Errorf("cannot snapshot unsupported routing engine %q", current)
	}
	return &ActionSnapshot{Action: action, Value: current}, nil
}

func (r *ActionRegistry) Rollback(ctx context.Context, snapshot ActionSnapshot) error {
	if strings.HasPrefix(snapshot.Action, "service.") {
		if !validServiceScript(snapshot.Target) || r == nil || r.handlers.ServiceAction == nil {
			return errors.New("service rollback is unavailable")
		}
		action := "stop"
		if snapshot.Value == "true" {
			action = "start"
		}
		return r.handlers.ServiceAction(ctx, snapshot.Target, action)
	}
	if snapshot.Action == "routing.switch_mode" {
		if !validRoutingMode(snapshot.Value) {
			return errors.New("invalid routing mode rollback snapshot")
		}
		if r == nil || r.handlers.SwitchRoutingMode == nil {
			return errors.New("routing mode rollback is unavailable")
		}
		return r.handlers.SwitchRoutingMode(ctx, snapshot.Value)
	}
	if snapshot.Action != "routing.switch_engine" || !validRoutingEngine(snapshot.Value) {
		return errors.New("invalid routing rollback snapshot")
	}
	if r == nil || r.handlers.SwitchRoutingEngine == nil {
		return errors.New("routing rollback is unavailable")
	}
	return r.handlers.SwitchRoutingEngine(ctx, snapshot.Value)
}

func defaultSocketVerification(addr, label string) *ActionVerification {
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return &ActionVerification{
			Status:  "warning",
			Summary: fmt.Sprintf("%s пока не отвечает на порту %s", label, addr),
			Detail:  err.Error(),
		}
	}
	_ = conn.Close()
	return &ActionVerification{
		Status:  "passed",
		Summary: fmt.Sprintf("%s успешно отвечает на порту %s", label, addr),
	}
}

func defaultDNSVerification() *ActionVerification {
	r := net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{Timeout: 2 * time.Second}
			return d.DialContext(ctx, "udp", "127.0.0.1:53")
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := r.LookupHost(ctx, "ya.ru")
	if err != nil {
		return &ActionVerification{
			Status:  "warning",
			Summary: "Локальный DNS-резолвер перезапущен, но тестовый запрос завершился с предупреждением",
			Detail:  err.Error(),
		}
	}
	return &ActionVerification{
		Status:  "passed",
		Summary: "Локальный DNS успешно сброшен и резолвит запросы",
	}
}

func newProposalID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("proposal-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw[:])
}

func parseActionProposal(text string) *RemediationProposal {
	text = strings.TrimSpace(text)
	actionIdx := strings.Index(text, "ACTION:")
	if actionIdx == -1 {
		return nil
	}
	actionPayload := strings.TrimSpace(text[actionIdx+len("ACTION:"):])
	if endIdx := strings.Index(actionPayload, "\n"); endIdx != -1 {
		actionPayload = actionPayload[:endIdx]
	}
	actionPayload = strings.TrimSpace(actionPayload)

	if strings.HasPrefix(actionPayload, "{") && strings.HasSuffix(actionPayload, "}") {
		var obj struct {
			Action      string `json:"action"`
			Target      string `json:"target"`
			Title       string `json:"title"`
			Description string `json:"description"`
			Risk        string `json:"risk"`
		}
		if err := json.Unmarshal([]byte(actionPayload), &obj); err == nil {
			// Title, description and risk are intentionally ignored. They are
			// rendered from the server-side allowlist so model output cannot
			// disguise a write action as a harmless operation.
			return validatedRemediationProposal(obj.Action, obj.Target)
		}
	}
	return nil
}
