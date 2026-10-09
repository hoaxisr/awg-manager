// Package aiassistant provides the safe, local diagnostic core used by the
// system AI assistant. It deliberately has no shell or configuration write
// access: all router data comes from the existing sanitized diagnostics runner.
package aiassistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/diagnostics"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

var ErrRunning = errors.New("AI diagnostics are already running")

type DiagnosticsRunner interface {
	RunWithStream(context.Context, diagnostics.RunOptions) (<-chan diagnostics.DiagEvent, error)
	Result() ([]byte, error)
}

type Finding struct {
	Severity       string `json:"severity"`
	Title          string `json:"title"`
	Detail         string `json:"detail"`
	Recommendation string `json:"recommendation"`
	Source         string `json:"source"`
}

type Stats struct {
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}

type ChatMessage struct {
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"createdAt"`
}

type State struct {
	Status      string               `json:"status"`
	Progress    string               `json:"progress,omitempty"`
	Question    string               `json:"question,omitempty"`
	Summary     string               `json:"summary,omitempty"`
	Engine      string               `json:"engine"`
	ReadOnly    bool                 `json:"readOnly"`
	StartedAt   time.Time            `json:"startedAt,omitempty"`
	CompletedAt time.Time            `json:"completedAt,omitempty"`
	Stats       Stats                `json:"stats"`
	Findings    []Finding            `json:"findings"`
	ModelAnswer string               `json:"modelAnswer,omitempty"`
	ModelError  string               `json:"modelError,omitempty"`
	Intent      Intent               `json:"intent"`
	ToolSteps   []ToolStep           `json:"toolSteps"`
	Error       string               `json:"error,omitempty"`
	Messages    []ChatMessage        `json:"messages"`
	Proposal    *RemediationProposal `json:"proposal,omitempty"`
}

type Service struct {
	runner       DiagnosticsRunner
	config       *ConfigStore
	model        ModelAnalyzer
	embedded     *EmbeddedManager
	tools        ToolExecutor
	actions      ActionExecutor
	memory       *MemoryStore
	lastFindings []Finding
	chatPath     string

	snapshotMgr SnapshotManager
	activeRun   *AgentRun

	mu    sync.RWMutex
	state State
}

func (s *Service) SetSnapshotManager(sm SnapshotManager) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshotMgr = sm
}

func (s *Service) SnapshotManager() SnapshotManager {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshotMgr
}

func (s *Service) SetActiveRun(run *AgentRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeRun = run
}

func (s *Service) ActiveRun() *AgentRun {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.activeRun == nil {
		return nil
	}
	return s.activeRun.Clone()
}

func (s *Service) SetModel(config *ConfigStore, model ModelAnalyzer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config = config
	s.model = model
}

func (s *Service) SetEmbedded(embedded *EmbeddedManager) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.embedded = embedded
}

func (s *Service) SetTools(tools ToolExecutor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tools = tools
}

func (s *Service) SetActions(actions ActionExecutor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.actions = actions
}

func (s *Service) SetMemory(memory *MemoryStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.memory = memory
}

func (s *Service) Memory() *MemoryStore {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.memory
}

func (s *Service) SetProposal(proposal *RemediationProposal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Proposal = proposal
}

func (s *Service) Proposal() *RemediationProposal {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state.Proposal
}

// ListTools exposes the same safe catalog used by the in-app agent. Returned
// definitions are descriptions only; execution still goes through CallTool.
func (s *Service) ListTools() []ToolDefinition {
	s.mu.RLock()
	tools := s.tools
	s.mu.RUnlock()
	if catalog, ok := tools.(ToolCatalog); ok {
		return catalog.Tools()
	}
	return []ToolDefinition{}
}

// CallTool is intentionally read-only. Mutating operations remain remediation
// proposals and require confirmation through ApplyAction.
func (s *Service) CallTool(ctx context.Context, call ToolCall) ToolStep {
	s.mu.RLock()
	tools := s.tools
	s.mu.RUnlock()
	if tools == nil {
		return ToolStep{Name: call.Name, Title: "Инструмент недоступен", Status: "error", Summary: "Реестр инструментов не настроен", ReadOnly: true, StartedAt: time.Now()}
	}
	return tools.Execute(ctx, call)
}

func NewService(runner DiagnosticsRunner) *Service {
	return &Service{
		runner: runner,
		state: State{
			Status:    "idle",
			Engine:    "local-diagnostics",
			ReadOnly:  true,
			Findings:  []Finding{},
			ToolSteps: []ToolStep{},
			Messages:  []ChatMessage{},
		},
	}
}

type persistedChat struct {
	Messages  []ChatMessage        `json:"messages"`
	Proposal  *RemediationProposal `json:"proposal,omitempty"`
	Question  string               `json:"question,omitempty"`
	Findings  []Finding            `json:"findings,omitempty"`
	Summary   string               `json:"summary,omitempty"`
	UpdatedAt time.Time            `json:"updatedAt"`
}

func (s *Service) SetChatStore(filePath string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chatPath = filePath
	if filePath != "" {
		if data, err := os.ReadFile(filePath); err == nil {
			var pc persistedChat
			if json.Unmarshal(data, &pc) == nil {
				if len(pc.Messages) > 0 {
					s.state.Messages = pc.Messages
				}
				if pc.Proposal != nil {
					s.state.Proposal = pc.Proposal
				}
				if pc.Question != "" {
					s.state.Question = pc.Question
				}
				if len(pc.Findings) > 0 {
					s.state.Findings = pc.Findings
					s.lastFindings = append([]Finding(nil), pc.Findings...)
				}
				if pc.Summary != "" {
					s.state.Summary = pc.Summary
				}
			}
		}
	}
}

func (s *Service) saveChatLocked() {
	if s.chatPath == "" {
		return
	}
	pc := persistedChat{
		Messages:  s.state.Messages,
		Proposal:  s.state.Proposal,
		Question:  s.state.Question,
		Findings:  s.state.Findings,
		Summary:   s.state.Summary,
		UpdatedAt: time.Now(),
	}
	raw, err := json.MarshalIndent(pc, "", "  ")
	if err == nil {
		_ = storage.AtomicWritePerm(s.chatPath, append(raw, '\n'), 0600)
	}
}

func (s *Service) ClearChat() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Status = "idle"
	s.state.Progress = ""
	s.state.Question = ""
	s.state.Summary = ""
	s.state.ModelAnswer = ""
	s.state.ModelError = ""
	s.state.Findings = []Finding{}
	s.state.ToolSteps = []ToolStep{}
	s.state.Messages = []ChatMessage{}
	s.state.Proposal = nil
	s.state.Error = ""
	s.activeRun = nil
	s.lastFindings = nil
	if s.chatPath != "" {
		_ = os.Remove(s.chatPath)
	}
}

func (s *Service) Start(question string) error {
	question = strings.TrimSpace(question)
	if len([]rune(question)) > 1000 {
		return fmt.Errorf("question is too long")
	}

	s.mu.Lock()
	if s.state.Status == "running" {
		s.mu.Unlock()
		return ErrRunning
	}
	if s.activeRun == nil || s.activeRun.Status == AgentRunSuccess || s.activeRun.Status == AgentRunFailed {
		s.activeRun = NewAgentRun(question)
	}
	messages := append([]ChatMessage(nil), s.state.Messages...)
	messages = appendChatMessage(messages, ChatMessage{Role: "user", Content: question, CreatedAt: time.Now()})
	preservedFindings := append([]Finding(nil), s.state.Findings...)
	if len(preservedFindings) == 0 && len(s.lastFindings) > 0 {
		preservedFindings = append([]Finding(nil), s.lastFindings...)
	}
	s.state = State{
		Status:    "running",
		Progress:  "ИИ-помощник обрабатывает сообщение…",
		Question:  question,
		Engine:    "local-diagnostics",
		ReadOnly:  true,
		StartedAt: time.Now(),
		Findings:  preservedFindings,
		ToolSteps: []ToolStep{},
		Messages:  messages,
	}
	s.saveChatLocked()
	s.mu.Unlock()

	go s.run()
	return nil
}

func (s *Service) Status() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state := s.state
	state.Findings = append([]Finding(nil), s.state.Findings...)
	state.ToolSteps = append([]ToolStep(nil), s.state.ToolSteps...)
	state.Messages = append([]ChatMessage(nil), s.state.Messages...)
	return state
}

func (s *Service) run() {
	s.mu.RLock()
	question, tools, configStore, model := s.state.Question, s.tools, s.config, s.model
	s.mu.RUnlock()

	// Native tool-capable providers must see the question before any local
	// keyword planner does. The model can then choose one or more tools,
	// inspect their results, and continue the conversation. The deterministic
	// planner below is only a fallback for providers without native tools or
	// when model analysis is disabled.
	if nativeAgentAvailable(configStore, model, tools) {
		s.mu.Lock()
		s.state.Intent = Intent{Kind: "agent"}
		s.state.Progress = "Модель решает, нужны ли проверки…"
		s.mu.Unlock()
		s.completeChat()
		return
	}

	// Если в запросе содержится конкретный код ошибки (Keenetic 0xcffd..., AmneziaWG H1=..., Reality uTLS, POSIX Errno и т.д.),
	// используем Базу Знаний Ошибок напрямую, не запуская случайный domain.inspect для 0.0.0.0 или номеров портов.
	if knownErr := LookupKnownError(question); knownErr != nil {
		s.mu.Lock()
		s.state.Intent = Intent{Kind: "error.known"}
		s.mu.Unlock()
		s.completeChat()
		return
	}
	if _, ok := FallbackHeuristicAnalysis(question); ok {
		s.mu.Lock()
		s.state.Intent = Intent{Kind: "error.heuristic"}
		s.mu.Unlock()
		s.completeChat()
		return
	}

	targeted := false
	intent := Intent{Kind: "diagnostics.general"}
	if tools != nil {
		var calls []ToolCall
		intent, calls = tools.Plan(question)
		targeted = len(calls) > 0
		s.mu.Lock()
		s.state.Intent = intent
		s.mu.Unlock()
		for _, call := range calls {
			s.mu.Lock()
			s.state.Progress = "Выполняется адресная проверка: " + call.Name
			s.mu.Unlock()
			step := tools.Execute(context.Background(), call)
			s.mu.Lock()
			s.state.ToolSteps = append(s.state.ToolSteps, step)
			s.mu.Unlock()
		}
	}
	if intent.Kind == "chat" {
		lowerQ := strings.ToLower(question)
		if containsAny(lowerQ, "отклонен", "ошибк", "проблем", "что не так") && len(s.state.Findings) == 0 && len(s.lastFindings) == 0 && s.runner != nil {
			// No diagnostics run yet in this session — run diagnostics to collect findings
		} else {
			s.completeChat()
			return
		}
	}
	if targeted {
		s.completeTargeted()
		return
	}

	if s.runner == nil {
		s.fail(errors.New("diagnostics runner is not configured"))
		return
	}
	s.mu.Lock()
	s.state.Progress = "Подготовка безопасной диагностики…"
	s.mu.Unlock()

	ch, err := s.runner.RunWithStream(context.Background(), diagnostics.RunOptions{IncludeRestart: false})
	if err != nil {
		s.fail(err)
		return
	}

	for event := range ch {
		s.mu.Lock()
		switch event.Type {
		case "phase":
			if event.Label != "" {
				s.state.Progress = event.Label
			}
		case "error":
			if event.Message != "" {
				s.state.Error = event.Message
			}
		}
		s.mu.Unlock()
	}

	raw, err := s.runner.Result()
	if err != nil {
		s.fail(err)
		return
	}
	var report diagnostics.Report
	if err := json.Unmarshal(raw, &report); err != nil {
		s.fail(fmt.Errorf("decode diagnostics report: %w", err))
		return
	}

	stats, findings := analyze(report)
	s.mu.RLock()
	toolSteps := append([]ToolStep(nil), s.state.ToolSteps...)
	s.mu.RUnlock()
	modelAnswer, modelErr, engineName := s.runModel(report, findings, toolSteps)
	s.completeDiagnosis(stats, findings, modelAnswer, modelErr, engineName)
}

func nativeAgentAvailable(configStore *ConfigStore, model ModelAnalyzer, tools ToolExecutor) bool {
	if configStore == nil || model == nil || tools == nil {
		return false
	}
	cfg := configStore.Get()
	if !cfg.Enabled {
		return false
	}
	providerSupported := cfg.Provider == "google" || cfg.Provider == "openai" || isChatCompletionsToolProvider(cfg)
	if !providerSupported {
		return false
	}
	if _, ok := model.(ToolAwareModel); !ok {
		return false
	}
	catalog, ok := tools.(ToolCatalog)
	return ok && len(catalog.Tools()) > 0
}

func (s *Service) completeDiagnosis(stats Stats, findings []Finding, modelAnswer string, modelErr error, engineName string) {
	s.mu.Lock()
	s.state.Status = "done"
	s.state.Progress = "Диагностика завершена"
	s.state.CompletedAt = time.Now()
	s.state.Stats = stats
	s.state.Findings = findings
	s.lastFindings = append([]Finding(nil), findings...)
	s.state.ModelAnswer = modelAnswer
	if modelErr != nil {
		s.state.ModelError = modelErr.Error()
	}
	if modelAnswer != "" && engineName != "" {
		s.state.Engine = engineName
	}
	s.state.Summary = buildSummary(stats, len(findings))
	proposal := remediationFromToolSteps(s.state.ToolSteps)
	if proposal == nil {
		proposal = remediationForFindings(findings, modelAnswer)
	}
	s.state.Proposal = proposal
	if proposal != nil && s.activeRun != nil {
		proposal.RunID = s.activeRun.ID
		s.activeRun.AddProposal(RemediationToChangeProposal(proposal, s.activeRun.ID))
	}
	answer := modelAnswer
	if answer == "" && modelErr != nil {
		answer = "Модельный анализ недоступен: " + modelErr.Error()
	}
	if answer == "" {
		if len(findings) > 0 {
			var b strings.Builder
			b.WriteString(fmt.Sprintf("**Обнаружено отклонений: %d** (успешно проверок: %d, пропущено: %d)\n\n", len(findings), stats.Passed, stats.Skipped))
			for i, f := range findings {
				sevBadge := "⚠️ Внимание"
				if f.Severity == "critical" || f.Severity == "error" {
					sevBadge = "❌ Ошибка"
				}
				b.WriteString(fmt.Sprintf("%d. **%s** (%s)\n", i+1, f.Title, sevBadge))
				if f.Detail != "" {
					b.WriteString(fmt.Sprintf("   - **Причина:** %s\n", f.Detail))
				}
				if f.Recommendation != "" {
					b.WriteString(fmt.Sprintf("   - **Рекомендация:** %s\n", f.Recommendation))
				}
				if f.Source != "" {
					b.WriteString(fmt.Sprintf("   - **Источник:** `%s`\n", f.Source))
				}
				combined := f.Title + " " + f.Detail + " " + f.Source
				if errRule := LookupKnownError(combined); errRule != nil && len(errRule.ActionSteps) > 0 {
					b.WriteString("   - **🖱️ Куда тыкнуть мышкой:**\n")
					for _, step := range errRule.ActionSteps {
						b.WriteString(fmt.Sprintf("     • %s\n", step))
					}
				}
				b.WriteByte('\n')
			}
			if proposal != nil {
				b.WriteString(fmt.Sprintf("💡 **Предлагаемое действие:** %s\n", proposal.Title))
			}
			answer = strings.TrimSpace(b.String())
		} else {
			answer = fmt.Sprintf("✅ **Диагностика завершена успешно:** все проверки пройдены (%d/%d).\nКритических отклонений в работе сети, туннелей и DNS не обнаружено.", stats.Passed, stats.Passed+stats.Skipped)
		}
		s.state.ModelAnswer = answer
	}
	s.state.Messages = appendChatMessage(s.state.Messages, ChatMessage{Role: "assistant", Content: answer, CreatedAt: time.Now()})
	s.state.Error = ""

	autoFixEnabled := false
	if s.config != nil {
		autoFixEnabled = s.config.Get().AutoFix
	}
	shouldAutoFix := autoFixEnabled && proposal != nil && autoFixActionAllowed(proposal.Action)
	if shouldAutoFix {
		proposal.AutoApplied = true
	}
	s.saveChatLocked()
	s.mu.Unlock()

	if shouldAutoFix {
		_ = s.ApplyAction(proposal.ID)
	}
}

// Auto-fix is intentionally narrower than the confirmed action catalog.
// Package changes, service control and routing mode/engine switches always
// require a human click even when the global auto-fix option is enabled.
func autoFixActionAllowed(action string) bool {
	switch action {
	case "mihomo.reload", "tunnel.restart", "subscription.update", "dns.flush":
		return true
	default:
		return false
	}
}

func remediationForFindings(findings []Finding, modelAnswer string) *RemediationProposal {
	if proposal := parseActionProposal(modelAnswer); proposal != nil {
		return proposal
	}
	for _, finding := range findings {
		switch finding.Source {
		case "singbox_proxy_port", "singbox_runtime":
			return &RemediationProposal{
				ID:          newProposalID(),
				Action:      "singbox.restart",
				Title:       "Перезапустить sing-box",
				Description: "Перезапустить процесс sing-box и заново поднять его локальные proxy-порты. Текущие соединения через sing-box кратковременно прервутся.",
				Risk:        "medium",
				Status:      "pending",
				CreatedAt:   time.Now(),
			}
		case "mihomo_runtime", "mihomo_api":
			return &RemediationProposal{
				ID:          newProposalID(),
				Action:      "mihomo.restart",
				Title:       "Перезапустить Mihomo",
				Description: "Перезапустить процесс Mihomo и переинициализировать сетевые правила.",
				Risk:        "medium",
				Status:      "pending",
				CreatedAt:   time.Now(),
			}
		case "tunnel_handshake_stale", "tunnel_down":
			target := finding.Detail
			return &RemediationProposal{
				ID:          newProposalID(),
				Action:      "tunnel.restart",
				Target:      target,
				Title:       "Перезапустить туннель " + target,
				Description: "Выполнить повторную инициализацию интерфейса туннеля и восстановить handshake.",
				Risk:        "low",
				Status:      "pending",
				CreatedAt:   time.Now(),
			}
		case "subscription_error":
			target := finding.Detail
			return &RemediationProposal{
				ID:          newProposalID(),
				Action:      "subscription.update",
				Target:      target,
				Title:       "Обновить подписку " + target,
				Description: "Принудительно загрузить обновлённый список серверов подписки.",
				Risk:        "low",
				Status:      "pending",
				CreatedAt:   time.Now(),
			}
		}
	}
	return nil
}

func (s *Service) ApplyAction(id string) error {
	return s.ApplyProposal(context.Background(), "", id)
}

func (s *Service) ApplyProposal(ctx context.Context, runID, proposalID string) error {
	s.mu.Lock()
	proposal := s.state.Proposal
	actions := s.actions
	activeRun := s.activeRun
	if proposal == nil || proposal.ID != proposalID || (proposal.Status != "pending" && !proposal.AutoApplied) {
		s.mu.Unlock()
		return errors.New("remediation proposal is unavailable or expired")
	}
	if runID != "" && proposal.RunID != "" && proposal.RunID != runID {
		s.mu.Unlock()
		return errors.New("remediation proposal belongs to an inactive or expired run")
	}
	if activeRun != nil && runID != "" && activeRun.ID != runID {
		s.mu.Unlock()
		return errors.New("remediation proposal belongs to an inactive or expired run")
	}
	if time.Since(proposal.CreatedAt) > 10*time.Minute {
		proposal.Status = "expired"
		s.mu.Unlock()
		return errors.New("remediation proposal expired")
	}
	spec, supported := remediationSpecs[proposal.Action]
	if !supported || !validRemediationTarget(proposal.Action, spec, proposal.Target) {
		proposal.Status = "failed"
		s.mu.Unlock()
		return errors.New("remediation proposal contains an unsupported action or target")
	}
	proposal.Status = "applying"
	s.state.ReadOnly = false
	if activeRun != nil {
		activeRun.AddJournal("apply", fmt.Sprintf("Применение: %s (цель: %s)", proposal.Title, proposal.Target), "", "")
	}
	s.mu.Unlock()

	if actions == nil {
		return s.finishAction(errors.New("remediation executor is unavailable"), nil)
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, actionTimeout(proposal.Action))
	defer cancel()

	var snapshot *ActionSnapshot
	transactional, hasTransaction := actions.(TransactionalActionExecutor)
	if hasTransaction {
		var snapshotErr error
		snapshot, snapshotErr = transactional.Snapshot(timeoutCtx, proposal.Action, proposal.Target)
		if snapshotErr != nil {
			return s.finishAction(fmt.Errorf("create rollback snapshot: %w", snapshotErr), nil)
		}
	}

	var sysSnap *SystemSnapshot
	if s.snapshotMgr != nil {
		var sysSnapErr error
		sysSnap, sysSnapErr = s.snapshotMgr.TakeSnapshot(timeoutCtx)
		if sysSnapErr != nil && !hasTransaction {
			return s.finishAction(fmt.Errorf("create system snapshot: %w", sysSnapErr), nil)
		}
	}

	err := actions.Apply(timeoutCtx, proposal.Action, proposal.Target)
	var ver *ActionVerification
	if err == nil {
		time.Sleep(1 * time.Second)
		ver, _ = actions.Verify(timeoutCtx, proposal.Action, proposal.Target)
	}
	shouldRollback := (snapshot != nil || sysSnap != nil) && (err != nil || (ver != nil && ver.Status == "failed"))
	if shouldRollback {
		var rollbackErr error
		if snapshot != nil && hasTransaction {
			rollbackErr = transactional.Rollback(timeoutCtx, *snapshot)
		}
		if sysSnap != nil && s.snapshotMgr != nil {
			sysErr := s.snapshotMgr.RestoreSnapshot(timeoutCtx, sysSnap)
			if rollbackErr == nil && sysErr != nil {
				rollbackErr = sysErr
			}
		}
		s.mu.Lock()
		if s.state.Proposal != nil {
			s.state.Proposal.RolledBack = (rollbackErr == nil)
			if rollbackErr != nil {
				s.state.Proposal.RollbackError = rollbackErr.Error()
			}
		}
		s.mu.Unlock()
		if err == nil && rollbackErr != nil {
			err = fmt.Errorf("verification failed and rollback failed: %w", rollbackErr)
		}
	}
	return s.finishAction(err, ver)
}

func actionTimeout(action string) time.Duration {
	if strings.HasPrefix(action, "opkg.") {
		return 6 * time.Minute
	}
	if strings.HasPrefix(action, "service.") || strings.HasPrefix(action, "routing.") {
		return 90 * time.Second
	}
	return 30 * time.Second
}

func (s *Service) finishAction(err error, ver *ActionVerification) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Proposal == nil {
		return err
	}
	proposal := s.state.Proposal
	activeRun := s.activeRun

	isFailure := err != nil || (ver != nil && ver.Status == "failed")

	if err != nil {
		if proposal.RolledBack {
			proposal.Status = "rolled_back"
			s.state.Messages = appendChatMessage(s.state.Messages, ChatMessage{
				Role:      "assistant",
				Content:   "Изменение не удалось применить. Предыдущее состояние автоматически восстановлено.",
				CreatedAt: time.Now(),
			})
		} else {
			proposal.Status = "failed"
		}
		proposal.Error = err.Error()
	} else {
		proposal.Status = "applied"
		proposal.Verification = ver
		msg := "Исправление применено: " + proposal.Title + "."
		if proposal.AutoApplied {
			msg = "ИИ автоматически применил исправление: " + proposal.Title + "."
		}
		if ver != nil {
			switch ver.Status {
			case "failed":
				proposal.Status = "verification_failed"
				msg = "Действие выполнено, но проблема не устранена: " + proposal.Title + "."
			case "warning":
				proposal.Status = "verification_warning"
				msg = "Действие выполнено, но результат требует дополнительной проверки: " + proposal.Title + "."
			}
			if ver.Summary != "" {
				msg += "\nПроверка: " + ver.Summary
			}
		}
		if proposal.RolledBack {
			proposal.Status = "rolled_back"
			msg += "\nПредыдущее состояние автоматически восстановлено."
		}
		s.state.Messages = appendChatMessage(s.state.Messages, ChatMessage{Role: "assistant", Content: msg, CreatedAt: time.Now()})
	}

	if activeRun != nil {
		if isFailure {
			errText := ""
			if err != nil {
				errText = err.Error()
			} else if ver != nil {
				errText = ver.Summary
			}
			rollbackStatus := "not_attempted"
			if proposal.RolledBack {
				rollbackStatus = "success"
			} else if proposal.RollbackError != "" {
				rollbackStatus = "failed: " + proposal.RollbackError
			}
			activeRun.AddJournal("apply_failed", "Действие не применилось или проверка не пройдена", errText, rollbackStatus)

			if activeRun.CanRetry() {
				activeRun.NextIteration()
				go s.continueAfterFailure(activeRun, proposal, err, ver)
			} else {
				activeRun.Complete(false, "Исчерпан лимит попыток автоматического исправления")
				s.state.Messages = appendChatMessage(s.state.Messages, ChatMessage{
					Role:      "assistant",
					Content:   fmt.Sprintf("Достигнут максимальный лимит попыток исправления (%d). Проблема требует ручного вмешательства.", activeRun.MaxIter),
					CreatedAt: time.Now(),
				})
			}
		} else {
			activeRun.Complete(true, "Исправление успешно применено и проверено")
			activeRun.AddJournal("verify", "Проверка успешно пройдена", "", "")
			s.recordAutoResolutionLocked(proposal, ver)
		}
	}

	s.saveChatLocked()
	return err
}

func (s *Service) recordAutoResolutionLocked(proposal *RemediationProposal, ver *ActionVerification) {
	mem := s.memory
	question := s.state.Question

	if mem == nil || proposal == nil {
		return
	}

	category := "routing"
	switch {
	case strings.HasPrefix(proposal.Action, "keenetic."):
		category = "network"
	case strings.HasPrefix(proposal.Action, "tunnel."):
		category = "tunnel"
	case strings.HasPrefix(proposal.Action, "service.") || strings.HasPrefix(proposal.Action, "opkg."):
		category = "system"
	case strings.HasPrefix(proposal.Action, "singbox.") || strings.HasPrefix(proposal.Action, "mihomo."):
		category = "routing"
	}

	trigger := strings.TrimSpace(question)
	if trigger == "" {
		trigger = proposal.Title
	}

	diag := proposal.Description
	if ver != nil && ver.Summary != "" {
		diag = ver.Summary
		if ver.Detail != "" {
			diag += " (" + ver.Detail + ")"
		}
	}

	pb := LearnedPlaybook{
		Category:     category,
		Title:        proposal.Title,
		Trigger:      trigger,
		Diagnosis:    diag,
		Action:       proposal.Action,
		Target:       proposal.Target,
		SuccessCount: 1,
		LearnedFrom:  "ai_assistant",
		CreatedAt:    time.Now(),
		LastUsedAt:   time.Now(),
	}
	mem.AddOrUpdatePlaybook(pb)

	actionDesc := proposal.Action
	if proposal.Target != "" {
		actionDesc = fmt.Sprintf("%s: %s", proposal.Action, proposal.Target)
	}
	mem.AddJournalEntry(LearningJournalEntry{
		ID:          genMemoryID(),
		Timestamp:   time.Now(),
		Trigger:     trigger,
		Query:       question,
		CloudAdvice: diag,
		ActionTaken: actionDesc,
		Outcome:     "success",
	})
}

func (s *Service) continueAfterFailure(run *AgentRun, failedProposal *RemediationProposal, applyErr error, ver *ActionVerification) {
	s.mu.Lock()
	errDetail := ""
	if applyErr != nil {
		errDetail = applyErr.Error()
	} else if ver != nil {
		errDetail = ver.Summary
		if ver.Detail != "" {
			errDetail += " (" + ver.Detail + ")"
		}
	}
	retryMsg := fmt.Sprintf(
		"⚠️ Изменение «%s» не применилось (%s).\n"+
			"↩️ Исходное состояние восстановлено из snapshot.\n"+
			"🔄 Анализирую причину сбоя (попытка %d из %d)...",
		failedProposal.Title, errDetail, run.Iteration, run.MaxIter,
	)
	s.state.Messages = appendChatMessage(s.state.Messages, ChatMessage{
		Role:      "assistant",
		Content:   retryMsg,
		CreatedAt: time.Now(),
	})
	s.state.Status = "running"
	s.state.Progress = fmt.Sprintf("ИИ анализирует причину сбоя (попытка %d/%d)…", run.Iteration, run.MaxIter)
	s.mu.Unlock()

	continuationPrompt := fmt.Sprintf(
		"Предыдущее действие '%s' (цель: '%s') завершилось сбоем: %s. "+
			"Система была возвращена к исходному состоянию (rollback). "+
			"Проанализируй причину ошибки, выполни необходимые диагностические проверки и предложи альтернативное исправление.",
		failedProposal.Action, failedProposal.Target, errDetail,
	)

	answer, modelErr, engineName := s.runModelWithQuestion(continuationPrompt, diagnostics.Report{}, nil, nil)

	s.mu.Lock()
	defer s.mu.Unlock()

	s.state.Status = "done"
	s.state.Progress = "Анализ завершён"
	s.state.CompletedAt = time.Now()
	if modelErr != nil && answer == "" {
		answer = "Модельный анализ недоступен: " + modelErr.Error()
	}
	s.state.ModelAnswer = answer
	if engineName != "" {
		s.state.Engine = engineName
	}
	s.state.Messages = appendChatMessage(s.state.Messages, ChatMessage{
		Role:      "assistant",
		Content:   answer,
		CreatedAt: time.Now(),
	})

	newProposal := remediationFromToolSteps(s.state.ToolSteps)
	if newProposal == nil {
		newProposal = remediationForFindings(nil, answer)
	}

	if newProposal != nil {
		newProposal.RunID = run.ID
		run.AddProposal(RemediationToChangeProposal(newProposal, run.ID))
		s.state.Proposal = newProposal
		run.AddJournal("propose", fmt.Sprintf("Предложено альтернативное исправление: %s", newProposal.Title), "", "")
	} else {
		run.Complete(false, answer)
		run.AddJournal("done", "Альтернативное исправление не найдено", "", "")
	}
	s.saveChatLocked()
}

func (s *Service) completeChat() {
	answer, modelErr, engineName := s.runModel(diagnostics.Report{}, nil, nil)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Status = "done"
	s.state.Progress = "Ответ готов"
	s.state.CompletedAt = time.Now()
	if answer == "" {
		if modelErr != nil {
			answer = "Модельный анализ недоступен: " + modelErr.Error()
		} else {
			answer = s.autonomousChatAnswer(s.state.Question, s.state.Findings)
			engineName = "autonomous-expert"
			s.state.Engine = engineName
		}
	}
	s.state.ModelAnswer = answer
	if answer != "" && engineName != "" {
		s.state.Engine = engineName
	}
	s.state.Messages = appendChatMessage(s.state.Messages, ChatMessage{Role: "assistant", Content: answer, CreatedAt: time.Now()})
	s.state.Proposal = remediationFromToolSteps(s.state.ToolSteps)
	if s.state.Proposal == nil {
		s.state.Proposal = remediationForFindings(nil, answer)
	}
	if s.state.Proposal != nil && s.activeRun != nil {
		s.state.Proposal.RunID = s.activeRun.ID
		s.activeRun.AddProposal(RemediationToChangeProposal(s.state.Proposal, s.activeRun.ID))
	}
	s.saveChatLocked()
}

func (s *Service) autonomousChatAnswer(question string, findings []Finding) string {
	// 0. Поиск по Базе Знаний Ошибок (KeeneticOS, AmneziaWG, Mihomo, Sing-box, DNS, System)
	if knownErr := LookupKnownError(question); knownErr != nil {
		return FormatErrorCard(knownErr, question)
	}
	if heuristicCard, ok := FallbackHeuristicAnalysis(question); ok {
		return heuristicCard
	}

	q := strings.ToLower(strings.TrimSpace(question))

	// Запрос о базе знаний / справочнике ошибок
	if containsAny(q, "база ошибок", "базу ошибок", "справочник", "каталог ошибок", "список ошибок") {
		return "📚 **Справочник и база знаний ошибок роутера**\n\n" +
			"В панели доступен специализированный раздел: вкладка **«Инструменты» -> «Система» -> «База ошибок»**.\n\n" +
			"Там собран полный каталог решений для:\n" +
			"• **KeeneticOS (NDMS):** все коды отказов `0xcffd...`, конфликты подсетей и AllowedIPs\n" +
			"• **AmneziaWG и WireGuard:** ошибки парсинга, лишние пробелы в `H1=`, таймауты handshake, MTU (122)\n" +
			"• **Mihomo и Sing-box:** конфликты портов `7890/10808/2080`, Reality uTLS, синтаксис YAML/JSON\n" +
			"• **Ядро Linux Errno:** ошибки сетевого стека, сокетов, памяти, прав доступа и накопителей\n\n" +
			"💡 Вы также можете прямо сюда в чат вставить любую строчку из системного журнала или консоли — я мгновенно распознаю её и покажу пошаговую инструкцию!"
	}

	// 1. Вопрос об обнаруженных отклонениях/ошибках
	if containsAny(q, "отклонен", "ошибк", "проблем", "что не так", "почему не работ", "детали") {
		if len(findings) == 0 && len(s.lastFindings) > 0 {
			findings = s.lastFindings
		}
		if len(findings) > 0 {
			var b strings.Builder
			b.WriteString(fmt.Sprintf("**Обнаружено отклонений: %d**\n\n", len(findings)))
			for i, f := range findings {
				sevBadge := "⚠️ Внимание"
				if f.Severity == "critical" || f.Severity == "error" {
					sevBadge = "❌ Ошибка"
				}
				b.WriteString(fmt.Sprintf("%d. **%s** (%s)\n", i+1, f.Title, sevBadge))
				if f.Detail != "" {
					b.WriteString(fmt.Sprintf("   - **Причина:** %s\n", f.Detail))
				}
				if f.Recommendation != "" {
					b.WriteString(fmt.Sprintf("   - **Рекомендация:** %s\n", f.Recommendation))
				}
				if f.Source != "" {
					b.WriteString(fmt.Sprintf("   - **Источник:** `%s`\n", f.Source))
				}
				combined := f.Title + " " + f.Detail + " " + f.Source
				if errRule := LookupKnownError(combined); errRule != nil && len(errRule.ActionSteps) > 0 {
					b.WriteString("   - **🖱️ Куда тыкнуть мышкой:**\n")
					for _, step := range errRule.ActionSteps {
						b.WriteString(fmt.Sprintf("     • %s\n", step))
					}
				}
				b.WriteByte('\n')
			}
			return strings.TrimSpace(b.String())
		}
		return "В текущей сессии активных отклонений не зафиксировано.\n\nВы можете запустить диагностику туннелей или ввести интересующий домен для проверки маршрутизации."
	}

	// 2. Приветствие
	if containsAny(q, "привет", "здравствуй", "добрый", "старт", "start", "hello", "hi", "хай") {
		return "Привет! Я автономный диагностический ассистент AWG Manager.\n\n" +
			"Даже без подключения внешнего ИИ я умею тестировать роутер прямо на месте:\n" +
			"- **Проверить туннели** — статус соединений, пинг шлюзов и handshake\n" +
			"- **Проверить DNS** — тестирование резолва через локальный DNS и TProxy\n" +
			"- **Статус маршрутизации** — активный движок (Mihomo / Sing-box), интерфейсы и правила\n" +
			"- **Показать отклонения** — детальный разбор обнаруженных сетевых сбоев\n\n" +
			"Выберите действие кнопками ниже или задайте вопрос."
	}

	// 3. Возможности / Справка
	if containsAny(q, "что ты умеешь", "что умеешь", "помощь", "справка", "help", "возможност", "команд") {
		return "**Возможности автономного помощника AWG Manager:**\n\n" +
			"**Диагностика сети и туннелей:**\n" +
			"- Мониторинг AmneziaWG, Wireguard, WDTT и OpenVPN соединений\n" +
			"- Проверка DNS-резолва и перехвата DNS через TProxy\n" +
			"- Проверка правил маршрутизации Sing-box / Mihomo\n" +
			"- Поиск конфликтов портов и сетевых интерфейсов\n\n" +
			"**Точечные проверки:**\n" +
			"- Введите любой домен (например: `youtube.com`), чтобы узнать маршрут и активный туннель\n" +
			"- Нажмите «Показать отклонения», чтобы разобрать найденные проблемы\n\n" +
			"**Диалог с нейросетью:**\n" +
			"- Для свободных бесед и генерации сложных скриптов вы можете подключить модель (Google Gemini, Anthropic Claude, DeepSeek или домашнюю Ollama) в сайдбаре слева."
	}

	// 4. Запросы о маршрутизации / туннелях / DNS
	if containsAny(q, "туннел", "vpn", "впн") {
		return "Для детальной проверки туннелей нажмите кнопку «Проверить туннели» выше. Я опрошу сетевые интерфейсы, пинг шлюзов и handshake."
	}
	if containsAny(q, "dns", "днс") {
		return "Для проверки DNS нажмите кнопку «Проверить DNS». Я протестирую резолв доменов через TProxy и локальные апстримы."
	}
	if containsAny(q, "маршрут", "движок", "singbox", "sing-box", "mihomo") {
		return "Для анализа правил и ядра нажмите «Статус маршрутизации». Я покажу текущий движок, режим работы и активные слоты."
	}

	// 5. Дефолтный ответ
	return "Для свободного диалога и генерации команд требуется подключение языковой модели (Google Gemini, Anthropic Claude, DeepSeek или локальной Ollama в сайдбаре слева).\n\n" +
		"В автономном режиме прямо сейчас доступны встроенные проверки:\n" +
		"- **Проверить туннели** — диагностика всех VPN-соединений\n" +
		"- **Проверить DNS** — тестирование резолва доменов\n" +
		"- **Статус маршрутизации** — проверка движка и правил\n" +
		"- **Показать отклонения** — список найденных сетевых проблем\n" +
		"- Введите любой домен (например `google.com`), чтобы увидеть его маршрут."
}

func (s *Service) completeTargeted() {
	s.mu.RLock()
	steps := append([]ToolStep(nil), s.state.ToolSteps...)
	s.mu.RUnlock()
	stats := Stats{}
	for _, step := range steps {
		switch step.Status {
		case "passed":
			stats.Passed++
		case "warning", "error":
			stats.Failed++
		default:
			stats.Skipped++
		}
	}
	modelAnswer, modelErr, engineName := s.runModel(diagnostics.Report{}, nil, steps)
	if modelAnswer == "" {
		modelAnswer = buildTargetedAnswer(steps)
		engineName = "typed-tools"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Status = "done"
	s.state.Progress = "Адресная проверка завершена"
	s.state.CompletedAt = time.Now()
	s.state.Stats = stats
	s.state.Summary = fmt.Sprintf("Выполнено адресных проверок: %d. Изменения не применялись.", len(steps))
	s.state.ModelAnswer = modelAnswer
	if modelErr != nil {
		s.state.ModelError = modelErr.Error()
	}
	if engineName != "" {
		s.state.Engine = engineName
	}
	s.state.Messages = appendChatMessage(s.state.Messages, ChatMessage{Role: "assistant", Content: s.state.ModelAnswer, CreatedAt: time.Now()})
	s.state.Proposal = remediationFromToolSteps(s.state.ToolSteps)
	if s.state.Proposal == nil {
		s.state.Proposal = remediationForFindings(nil, modelAnswer)
	}
	s.saveChatLocked()
}

func remediationFromToolSteps(steps []ToolStep) *RemediationProposal {
	for index := len(steps) - 1; index >= 0; index-- {
		step := steps[index]
		if step.Name != "remediation.propose" || step.Status != "passed" || len(step.Evidence) == 0 {
			continue
		}
		var proposed RemediationProposal
		if json.Unmarshal([]byte(step.Evidence[0]), &proposed) == nil {
			return validatedRemediationProposal(proposed.Action, proposed.Target)
		}
	}
	return nil
}

func appendChatMessage(messages []ChatMessage, message ChatMessage) []ChatMessage {
	message.Content = truncateModelText(sanitizeChatMessage(strings.TrimSpace(message.Content)), 4000)
	if message.Content == "" {
		return messages
	}
	messages = append(messages, message)
	const maxMessages = 12
	if len(messages) > maxMessages {
		messages = append([]ChatMessage(nil), messages[len(messages)-maxMessages:]...)
	}
	return messages
}

func buildTargetedAnswer(steps []ToolStep) string {
	if len(steps) == 0 {
		return "Адресные проверки не выполнялись."
	}
	var builder strings.Builder
	for i, step := range steps {
		if i > 0 {
			builder.WriteString("\n\n")
		}
		if step.Name != "domain.inspect" || len(step.Evidence) == 0 {
			builder.WriteString(step.Summary)
		}

		if step.Name == "tunnels.list" && len(step.Evidence) > 0 {
			var tunnels []struct {
				ID        string `json:"id"`
				Name      string `json:"name"`
				Interface string `json:"interface"`
				Enabled   bool   `json:"enabled"`
				Type      string `json:"type"`
				RxBytes   int64  `json:"rxBytes"`
				TxBytes   int64  `json:"txBytes"`
			}
			if json.Unmarshal([]byte(step.Evidence[0]), &tunnels) == nil && len(tunnels) > 0 {
				builder.WriteString(":\n")
				for _, t := range tunnels {
					status := "Включен"
					if !t.Enabled {
						status = "Отключен"
					}
					builder.WriteString(fmt.Sprintf("- **%s** (`%s`): %s", t.Name, t.Interface, status))
					if t.RxBytes > 0 || t.TxBytes > 0 {
						builder.WriteString(fmt.Sprintf(", трафик: ↓%s / ↑%s", formatBytesHelper(t.RxBytes), formatBytesHelper(t.TxBytes)))
					}
					builder.WriteByte('\n')
				}
				continue
			}
		}

		if step.Name == "domain.inspect" && len(step.Evidence) > 0 {
			domainLine := step.Evidence[0]
			domainName := ""
			resolvedIPs := ""
			if parts := strings.Split(domainLine, " -> "); len(parts) == 2 {
				domainName = strings.TrimSpace(parts[0])
				resolvedIPs = strings.TrimSpace(parts[1])
			}
			if domainName == "" {
				domainName = "запрошенного домена"
			}

			interfaces := make(map[string]bool)
			hasVPN := false
			hasDirect := false
			hasUnreachable := false
			for _, ev := range step.Evidence[1:] {
				if strings.Contains(strings.ToLower(ev), "unreachable") {
					hasUnreachable = true
					continue
				}
				if idx := strings.Index(ev, " dev "); idx != -1 {
					sub := ev[idx+5:]
					fields := strings.Fields(sub)
					if len(fields) > 0 {
						iface := fields[0]
						interfaces[iface] = true
						if strings.HasPrefix(iface, "opkgtun") || strings.HasPrefix(iface, "nwg") || strings.HasPrefix(iface, "tun") || strings.HasPrefix(iface, "wg") {
							hasVPN = true
						} else {
							hasDirect = true
						}
					}
				}
			}

			var b strings.Builder
			b.WriteString(fmt.Sprintf("**Маршрутизация для %s:**\n\n", domainName))
			if resolvedIPs != "" {
				b.WriteString(fmt.Sprintf("- **DNS-резолв:** успешно (`%s`)\n", resolvedIPs))
			}
			if len(interfaces) > 0 {
				var ifaceList []string
				for iface := range interfaces {
					ifaceList = append(ifaceList, fmt.Sprintf("`%s`", iface))
				}
				sort.Strings(ifaceList)
				b.WriteString(fmt.Sprintf("- **Сетевой интерфейс:** %s\n", strings.Join(ifaceList, ", ")))
			}

			if hasUnreachable {
				b.WriteString("- **Статус:** ❌ **Маршрут недоступен**\n\n")
				b.WriteString("Роутер не нашёл маршрут для отправки пакетов к этому ресурсу.")
			} else if hasVPN && !hasDirect {
				b.WriteString("- **Статус:** ✅ **Трафик направляется через VPN**\n\n")
				b.WriteString(fmt.Sprintf("Пакеты к `%s` идут через защищённый туннель.", domainName))
			} else if hasVPN && hasDirect {
				b.WriteString("- **Статус:** ⚠️ **Гибридный маршрут (часть IP идёт через VPN, часть напрямую)**\n\n")
				b.WriteString("Некоторые IP-адреса направляются в туннель, а другие — напрямую через провайдера.")
			} else {
				b.WriteString("- **Статус:** ⚠️ **Прямое подключение через провайдера (без VPN)**\n\n")
				b.WriteString(fmt.Sprintf("Трафик к `%s` идёт напрямую через вашего интернет-провайдера (в обход туннелей).\n\nЕсли ресурс заблокирован или должен работать через VPN — добавьте `%s` в список доменов на вкладке «Маршрутизация».", domainName, domainName))
			}
			builder.WriteString(b.String())
			continue
		}

		if step.Name == "dns.inspect" && len(step.Evidence) > 0 {
			builder.WriteString(":\n")
			for _, ev := range step.Evidence {
				builder.WriteString(fmt.Sprintf("- %s\n", ev))
			}
			builder.WriteString("\nDNS-резолвер роутера отвечает корректно.")
			continue
		}

		if len(step.Evidence) > 0 {
			builder.WriteString(":\n")
			for _, evidence := range step.Evidence {
				builder.WriteString("- ")
				builder.WriteString(evidence)
				builder.WriteByte('\n')
			}
		}
	}
	return strings.TrimSpace(builder.String())
}

func formatBytesHelper(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

func (s *Service) runModel(report diagnostics.Report, findings []Finding, toolSteps []ToolStep) (string, error, string) {
	s.mu.RLock()
	question := s.state.Question
	s.mu.RUnlock()
	return s.runModelWithQuestion(question, report, findings, toolSteps)
}

func (s *Service) runModelWithQuestion(question string, report diagnostics.Report, findings []Finding, toolSteps []ToolStep) (string, error, string) {
	s.mu.RLock()
	configStore, model, embedded, tools := s.config, s.model, s.embedded, s.tools
	messages := append([]ChatMessage(nil), s.state.Messages...)
	s.mu.RUnlock()
	if configStore == nil || model == nil {
		return "", nil, ""
	}
	cfg := configStore.Get()
	if !cfg.Enabled {
		return "", nil, ""
	}
	s.mu.Lock()
	if len(toolSteps) > 0 || len(findings) > 0 || len(report.Tests) > 0 {
		s.state.Progress = "Модель анализирует результаты проверок…"
	} else {
		s.state.Progress = "Модель готовит ответ…"
	}
	s.mu.Unlock()

	timeout := 60 * time.Second
	switch cfg.Provider {
	case "local_embedded":
		timeout = 240 * time.Second
	case "google":
		timeout = 75 * time.Second
	case "ollama":
		timeout = 180 * time.Second
	case "custom":
		timeout = 180 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if cfg.Provider == "local_embedded" && embedded != nil {
		if err := embedded.EnsureRunning(ctx); err != nil {
			return "", fmt.Errorf("local model engine: %w", err), cfg.Provider
		}
	}

	definitions := []ToolDefinition{}
	if catalog, ok := tools.(ToolCatalog); ok {
		definitions = catalog.Tools()
	}
	baseQuestion := conversationalQuestion(question, messages)
	s.mu.RLock()
	mem := s.memory
	s.mu.RUnlock()
	if mem != nil {
		if promptCtx := mem.RenderPromptContext(); promptCtx != "" {
			baseQuestion = promptCtx + "\n\n" + baseQuestion
		}
	}
	steps := append([]ToolStep(nil), toolSteps...)
	if nativeModel, ok := model.(ToolAwareModel); ok && len(definitions) > 0 && (cfg.Provider == "openai" || cfg.Provider == "google" || isChatCompletionsToolProvider(cfg)) {
		return s.runNativeToolLoop(ctx, cfg, nativeModel, baseQuestion, report, findings, steps, definitions, tools)
	}
	seen := map[string]bool{}
	for turn := 0; turn < maxAgentToolTurns; turn++ {
		payload, err := buildModelDiagnostic(report, findings, steps)
		if err != nil {
			return "", err, cfg.Provider
		}
		ans, modelErr := model.Analyze(ctx, cfg, agentPrompt(baseQuestion, definitions), payload)
		if modelErr != nil {
			return "", modelErr, cfg.Provider
		}
		call, wantsTool := parseModelToolCall(ans)
		if !wantsTool || tools == nil {
			return ans, nil, cfg.Provider
		}
		key := toolCallKey(call)
		if seen[key] {
			return "Не удалось продолжить анализ: модель повторно запросила тот же инструмент.", nil, cfg.Provider
		}
		seen[key] = true
		s.mu.Lock()
		s.state.Progress = "ИИ проверяет: " + call.Name
		s.mu.Unlock()
		step := tools.Execute(ctx, call)
		steps = append(steps, step)
		s.mu.Lock()
		s.state.ToolSteps = append(s.state.ToolSteps, step)
		s.mu.Unlock()
	}
	summary := buildTargetedAnswer(steps)
	continuationMsg := fmt.Sprintf("%s\n\n---\n**Достигнут лимит автоматической диагностики (%d шагов).**\nЕсли требуется продолжить углублённое исследование, напишите «Продолжай» или задайте уточняющий вопрос, и я продолжу расследование на основе уже собранных фактов.", summary, maxAgentToolTurns)
	return continuationMsg, nil, cfg.Provider
}

func (s *Service) runNativeToolLoop(
	ctx context.Context,
	cfg ModelConfig,
	model ToolAwareModel,
	question string,
	report diagnostics.Report,
	findings []Finding,
	steps []ToolStep,
	definitions []ToolDefinition,
	tools ToolExecutor,
) (string, error, string) {
	history := []ModelToolExchange{}
	seen := map[string]bool{}
	totalCalls := 0
	payload, err := buildModelDiagnostic(report, findings, steps)
	if err != nil {
		return "", err, cfg.Provider
	}
	for turnIndex := 0; turnIndex < maxAgentToolTurns; turnIndex++ {
		turn, err := model.AnalyzeWithTools(ctx, cfg, question, payload, definitions, history)
		if err != nil {
			return "", err, cfg.Provider
		}
		if len(turn.Calls) == 0 {
			if strings.TrimSpace(turn.Text) == "" {
				return "Модель завершила анализ без текстового ответа.", nil, cfg.Provider
			}
			return turn.Text, nil, cfg.Provider
		}
		exchange := ModelToolExchange{ProviderState: turn.ProviderState}
		for _, nativeCall := range turn.Calls {
			if totalCalls >= maxAgentToolTurns {
				exchange.Results = append(exchange.Results, ModelToolResult{
					ID:     nativeCall.ID,
					Name:   nativeCall.Name,
					Output: `{"status":"skipped","summary":"Лимит шагов автоматической диагностики исчерпан. Пожалуйста, сформулируйте итоговый ответ на основе всех уже собранных фактов."}`,
				})
				continue
			}
			arguments := make(map[string]string, len(nativeCall.Arguments))
			for key, value := range nativeCall.Arguments {
				switch typed := value.(type) {
				case string:
					arguments[key] = typed
				case float64, bool:
					arguments[key] = fmt.Sprint(typed)
				}
			}
			call := ToolCall{Name: nativeCall.Name, Arguments: arguments}
			key := toolCallKey(call)
			if seen[key] {
				step := ToolStep{
					Name:      call.Name,
					Title:     call.Name,
					Status:    "warning",
					ReadOnly:  true,
					StartedAt: time.Now(),
					Summary:   "Инструмент уже вызывался с такими параметрами",
					Evidence:  []string{"Повторный вызов отклонён: используйте предыдущие результаты."},
				}
				output, _ := json.Marshal(step)
				exchange.Results = append(exchange.Results, ModelToolResult{
					ID: nativeCall.ID, Name: nativeCall.Name, Output: string(output),
				})
				continue
			}
			seen[key] = true
			s.mu.Lock()
			s.state.Progress = "ИИ проверяет: " + call.Name
			s.mu.Unlock()
			step := tools.Execute(ctx, call)
			steps = append(steps, step)
			s.mu.Lock()
			s.state.ToolSteps = append(s.state.ToolSteps, step)
			s.mu.Unlock()
			output, _ := json.Marshal(sanitizeToolSteps([]ToolStep{step})[0])
			exchange.Results = append(exchange.Results, ModelToolResult{
				ID: nativeCall.ID, Name: nativeCall.Name, Output: string(output),
			})
			totalCalls++
		}
		history = append(history, exchange)
		s.mu.Lock()
		s.state.Progress = "Модель анализирует результаты проверки…"
		s.mu.Unlock()
		if totalCalls >= maxAgentToolTurns {
			break
		}
	}
	// Give model one final chance to formulate its conclusive diagnosis based on all gathered history.
	finalTurn, finalErr := model.AnalyzeWithTools(ctx, cfg, question, payload, definitions, history, "none")
	if finalErr == nil && strings.TrimSpace(finalTurn.Text) != "" {
		return finalTurn.Text, nil, cfg.Provider
	}

	summary := buildTargetedAnswer(steps)
	continuationMsg := fmt.Sprintf("%s\n\n---\n**Достигнут лимит автоматической диагностики (%d шагов).**\nЕсли требуется продолжить углублённое исследование, напишите «Продолжай» или задайте уточняющий вопрос, и я продолжу расследование на основе уже собранных фактов.", summary, maxAgentToolTurns)
	return continuationMsg, nil, cfg.Provider
}

func conversationalQuestion(current string, messages []ChatMessage) string {
	if len(messages) <= 1 {
		return current
	}
	start := len(messages) - 7
	if start < 0 {
		start = 0
	}
	var b strings.Builder
	b.WriteString("Краткая история диалога:\n")
	for _, message := range messages[start : len(messages)-1] {
		if message.Role != "user" && message.Role != "assistant" {
			continue
		}
		b.WriteString(message.Role)
		b.WriteString(": ")
		b.WriteString(truncateModelText(sanitizeModelText(message.Content), 500))
		b.WriteByte('\n')
	}
	b.WriteString("Текущий вопрос: ")
	b.WriteString(current)
	return b.String()
}

func buildModelDiagnostic(report diagnostics.Report, findings []Finding, toolSteps []ToolStep) ([]byte, error) {
	type compactCheck struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Detail string `json:"detail,omitempty"`
	}
	checks := make([]compactCheck, 0, len(report.Tests))
	for _, t := range report.Tests {
		c := compactCheck{Name: t.Name, Status: string(t.Status)}
		if t.Status != "pass" && t.Detail != "" {
			detail := sanitizeModelText(t.Detail)
			detail = truncateModelText(detail, 120)
			c.Detail = detail
		}
		checks = append(checks, c)
	}

	modelFindings := make([]Finding, 0, len(findings))
	for _, f := range findings {
		modelFindings = append(modelFindings, Finding{
			Severity:       f.Severity,
			Title:          sanitizeModelText(f.Title),
			Detail:         sanitizeModelText(f.Detail),
			Recommendation: f.Recommendation,
			Source:         f.Source,
		})
	}

	payload := struct {
		System struct {
			Arch          string `json:"arch"`
			MemoryMB      int    `json:"memoryMB"`
			TotalChecks   int    `json:"totalChecks"`
			RoutingEngine string `json:"routingEngine,omitempty"`
		} `json:"system"`
		Checks   []compactCheck `json:"checks"`
		Findings []Finding      `json:"findings,omitempty"`
		Tools    []ToolStep     `json:"targetedTools,omitempty"`
	}{
		System: struct {
			Arch          string `json:"arch"`
			MemoryMB      int    `json:"memoryMB"`
			TotalChecks   int    `json:"totalChecks"`
			RoutingEngine string `json:"routingEngine,omitempty"`
		}{
			Arch:          report.System.Arch,
			MemoryMB:      report.System.TotalMemoryMB,
			TotalChecks:   len(report.Tests),
			RoutingEngine: report.System.Backend,
		},
		Checks:   checks,
		Findings: modelFindings,
		Tools:    sanitizeToolSteps(toolSteps),
	}
	return json.Marshal(payload)
}

func sanitizeToolSteps(steps []ToolStep) []ToolStep {
	result := make([]ToolStep, len(steps))
	for i, step := range steps {
		result[i] = step
		result[i].Summary = sanitizeModelText(step.Summary)
		result[i].Evidence = make([]string, len(step.Evidence))
		for j, evidence := range step.Evidence {
			result[i].Evidence[j] = sanitizeModelText(evidence)
		}
	}
	return result
}

var (
	modelIPv4Pattern   = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	modelIPv6Pattern   = regexp.MustCompile(`(?i)\b(?:[0-9a-f]{1,4}:){2,}[0-9a-f:]{0,39}\b`)
	modelAPIKeyPattern = regexp.MustCompile(`(?i)\b(?:sk-[a-z0-9_-]{8,}|bearer\s+[a-z0-9._~+/-]{8,}|api[_-]?key\s*[:=]\s*\S+)`)
	modelWGKeyPattern  = regexp.MustCompile(`\b[A-Za-z0-9+/]{42,44}={0,2}\b`)
)

func sanitizeModelText(value string) string {
	value = modelIPv4Pattern.ReplaceAllString(value, "PRIVATE-IP")
	value = modelIPv6Pattern.ReplaceAllString(value, "PRIVATE-IPV6")
	value = modelAPIKeyPattern.ReplaceAllString(value, "[SECRET]")
	return modelWGKeyPattern.ReplaceAllString(value, "[KEY]")
}

func sanitizeChatMessage(value string) string {
	value = modelAPIKeyPattern.ReplaceAllString(value, "[SECRET]")
	return modelWGKeyPattern.ReplaceAllString(value, "[KEY]")
}

func truncateModelText(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "..."
}

func (s *Service) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Status = "error"
	s.state.Progress = "Диагностика не завершена"
	s.state.CompletedAt = time.Now()
	s.state.Error = err.Error()
	s.state.Messages = appendChatMessage(s.state.Messages, ChatMessage{Role: "assistant", Content: "Не удалось завершить диагностику: " + err.Error(), CreatedAt: time.Now()})
	s.saveChatLocked()
}

func analyze(report diagnostics.Report) (Stats, []Finding) {
	stats := Stats{}
	findings := make([]Finding, 0)
	for _, test := range report.Tests {
		switch test.Status {
		case diagnostics.StatusPass:
			stats.Passed++
		case diagnostics.StatusSkip:
			stats.Skipped++
		case diagnostics.StatusFail, diagnostics.StatusError:
			stats.Failed++
			findings = append(findings, findingForTest(test))
		}
	}

	for _, issue := range report.BootHealth.NotStartedOnBoot {
		findings = append(findings, Finding{
			Severity:       "warning",
			Title:          "Туннель не запустился после загрузки: " + issue.TunnelName,
			Detail:         "Туннель включён и ожидается запущенным, но активного интерфейса нет.",
			Recommendation: "Проверьте автозапуск туннеля и журнал загрузки. Перед перезапуском сохраните текущую конфигурацию.",
			Source:         "boot_health",
		})
	}

	if findings == nil {
		findings = []Finding{}
	}
	return stats, findings
}

func findingForTest(test diagnostics.TestResult) Finding {
	severity := "warning"
	if test.Status == diagnostics.StatusError || isCriticalTest(test.Name) {
		severity = "critical"
	}
	return Finding{
		Severity:       severity,
		Title:          test.Description,
		Detail:         test.Detail,
		Recommendation: recommendation(test.Name),
		Source:         test.Name,
	}
}

func isCriticalTest(name string) bool {
	switch name {
	case "wan_connectivity", "ndms_health", "direct_connectivity", "singbox_runtime", "interface_state_consistency":
		return true
	default:
		return false
	}
}

func recommendation(name string) string {
	switch name {
	case "wan_connectivity":
		return "Проверьте состояние провайдера и наличие default route. Не меняйте policy routing, пока базовый WAN не восстановлен."
	case "ndms_health":
		return "Проверьте доступность NDMS и журнал запуска AWG Manager."
	case "direct_connectivity":
		return "Сначала восстановите прямой доступ с роутера, затем проверяйте прокси-маршруты."
	case "singbox_runtime":
		return "Проверьте выбранный движок, его конфигурацию и последние ошибки запуска."
	case "endpoint_route_check", "route_leak_check":
		return "Сопоставьте ip rule, таблицы маршрутизации и интерфейс выхода. Изменения следует применять по одному с возможностью отката."
	case "firewall_rules", "rp_filter":
		return "Проверьте правила маркировки/перехвата и rp_filter для задействованных интерфейсов."
	case "dns_resolve", "dns_leak_check":
		return "Проверьте DNS-маршрут, перехват порта 53 и доступность настроенных upstream-серверов."
	case "awg_handshake", "endpoint_reachable", "tunnel_connectivity":
		return "Проверьте маршрут до endpoint, время на роутере, ключи и доступность удалённого сервера."
	default:
		return "Откройте подробности проверки и журнал. До подтверждения причины не применяйте автоматические изменения."
	}
}

func buildSummary(stats Stats, findings int) string {
	if findings == 0 {
		return fmt.Sprintf("Проверки завершены: %d успешно, критических отклонений не обнаружено.", stats.Passed)
	}
	return fmt.Sprintf("Обнаружено отклонений: %d. Успешно: %d, пропущено: %d. Исправления не применялись.", findings, stats.Passed, stats.Skipped)
}
