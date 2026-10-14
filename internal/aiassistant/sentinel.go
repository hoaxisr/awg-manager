package aiassistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// SentinelStatus provides runtime status of the autonomous background sentinel.
type SentinelStatus struct {
	Running         bool      `json:"running"`
	Enabled         bool      `json:"enabled"`
	AutonomyLevel   string    `json:"autonomyLevel"`
	IntervalSeconds int       `json:"intervalSeconds"`
	LastCheck       time.Time `json:"lastCheck,omitempty"`
	LastSymptom     string    `json:"lastSymptom,omitempty"`
	LastAction      string    `json:"lastAction,omitempty"`
	LastError       string    `json:"lastError,omitempty"`
}

// Sentinel is the autonomous watchdog that monitors network health, consults
// long-term memory for learned playbooks, queries Cloud AI on novel failures,
// and records learning events into the journal.
type Sentinel struct {
	service         *Service
	memory          *MemoryStore
	sources         ToolSources
	actions         ActionExecutor
	mu              sync.RWMutex
	running         bool
	cancel          context.CancelFunc
	lastCheck       time.Time
	lastSymptom     string
	lastAction      string
	lastError       string
	recentSymptoms  map[string]time.Time
	recentCloudHits map[string]time.Time
}

func NewSentinel(service *Service, memory *MemoryStore, sources ToolSources, actions ActionExecutor) *Sentinel {
	return &Sentinel{
		service:         service,
		memory:          memory,
		sources:         sources,
		actions:         actions,
		recentSymptoms:  make(map[string]time.Time),
		recentCloudHits: make(map[string]time.Time),
	}
}

// Start begins the background sentinel monitoring loop.
func (s *Sentinel) Start() {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.running = true
	s.mu.Unlock()

	go s.loop(ctx)
}

// Stop stops the background sentinel monitoring loop.
func (s *Sentinel) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return
	}
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.running = false
}

// Status returns a snapshot of the Sentinel state.
func (s *Sentinel) Status() SentinelStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	settings := defaultSentinelSettings()
	if s.memory != nil {
		settings = s.memory.Settings()
	}

	return SentinelStatus{
		Running:         s.running,
		Enabled:         settings.Enabled,
		AutonomyLevel:   settings.AutonomyLevel,
		IntervalSeconds: settings.IntervalSeconds,
		LastCheck:       s.lastCheck,
		LastSymptom:     s.lastSymptom,
		LastAction:      s.lastAction,
		LastError:       s.lastError,
	}
}

func (s *Sentinel) loop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// Initial delay so router startup settles
	select {
	case <-ctx.Done():
		return
	case <-time.After(15 * time.Second):
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runCheck(ctx)
		}
	}
}

func (s *Sentinel) runCheck(ctx context.Context) {
	if s.memory == nil {
		return
	}
	settings := s.memory.Settings()
	if !settings.Enabled || settings.AutonomyLevel == "disabled" {
		return
	}

	s.mu.Lock()
	s.lastCheck = time.Now()
	s.mu.Unlock()

	symptom := s.probeHealth(ctx)
	if symptom == "" {
		s.mu.Lock()
		s.lastSymptom = ""
		s.lastError = ""
		s.mu.Unlock()
		return
	}

	s.mu.Lock()
	s.lastSymptom = symptom
	lastSeen, exists := s.recentSymptoms[symptom]
	if exists && time.Since(lastSeen) < 5*time.Minute {
		// Cooldown for repeated symptom
		s.mu.Unlock()
		return
	}
	s.recentSymptoms[symptom] = time.Now()
	s.mu.Unlock()

	s.handleSymptom(ctx, symptom, settings)
}

func (s *Sentinel) probeHealth(ctx context.Context) string {
	// 1. Probe active routing engine
	if s.sources.EngineStatus != nil {
		statusRaw, err := s.sources.EngineStatus(ctx)
		if err == nil && statusRaw != nil {
			if str, ok := statusRaw.(string); ok {
				if strings.Contains(str, "stopped") || strings.Contains(str, "failed") {
					return "engine_stopped: " + str
				}
			} else {
				rawBytes, _ := json.Marshal(statusRaw)
				var st struct {
					Routing struct {
						Enabled        bool   `json:"enabled"`
						SelectedEngine string `json:"selectedEngine"`
					} `json:"routing"`
					Singbox struct {
						Running   bool   `json:"running"`
						Installed bool   `json:"installed"`
						LastError string `json:"lastError"`
					} `json:"singbox"`
					Mihomo struct {
						Running   bool   `json:"running"`
						Enabled   bool   `json:"enabled"`
						Selected  bool   `json:"selected"`
						Active    bool   `json:"active"`
						LastError string `json:"lastError"`
					} `json:"mihomo"`
				}
				if json.Unmarshal(rawBytes, &st) == nil {
					engine := strings.ToLower(strings.TrimSpace(st.Routing.SelectedEngine))
					if engine == "" {
						if st.Mihomo.Selected || st.Mihomo.Enabled || st.Mihomo.Active {
							engine = "mihomo"
						} else {
							engine = "sing-box"
						}
					}
					if engine == "mihomo" {
						if !st.Mihomo.Running && (st.Mihomo.Enabled || st.Routing.Enabled || st.Mihomo.Selected) {
							return "engine_stopped: mihomo is stopped"
						}
					} else if engine == "sing-box" || engine == "singbox" {
						if !st.Singbox.Running && st.Routing.Enabled {
							return "engine_stopped: sing-box is stopped"
						}
					}
				}
			}
		}
	}

	// 2. Probe tunnels
	if s.sources.Tunnels != nil {
		tunnelsRaw, err := s.sources.Tunnels(ctx)
		if err == nil && tunnelsRaw != nil {
			rawBytes, _ := json.Marshal(tunnelsRaw)
			var tunnels []map[string]any
			if json.Unmarshal(rawBytes, &tunnels) == nil {
				for _, t := range tunnels {
					enabled, _ := t["enabled"].(bool)
					state, _ := t["state"].(string)
					name, _ := t["name"].(string)
					id, _ := t["id"].(string)
					if enabled && (state == "failed" || state == "error" || state == "down") {
						target := id
						if target == "" {
							target = name
						}
						return fmt.Sprintf("tunnel_down: %s", target)
					}
				}
			}
		}
	}

	// 3. Probe DNS resolution
	if s.sources.DNSProbe != nil {
		if err := s.sources.DNSProbe(ctx); err != nil {
			return "dns_resolution_failed: " + err.Error()
		}
		return ""
	}

	r := net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{Timeout: 2 * time.Second}
			return d.DialContext(ctx, "udp", "127.0.0.1:53")
		},
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err := r.LookupHost(probeCtx, "cp.cloudflare.com")
	if err != nil {
		return "dns_resolution_failed: 127.0.0.1:53"
	}

	return ""
}

func (s *Sentinel) handleSymptom(ctx context.Context, symptom string, settings SentinelSettings) {
	// Check long-term memory for existing playbook
	pb := s.memory.FindPlaybook(symptom)
	if pb != nil {
		s.applyPlaybook(ctx, pb, symptom, settings)
		return
	}

	// No playbook found: consult Cloud AI if configured
	s.consultCloudAI(ctx, symptom, settings)
}

func (s *Sentinel) applyPlaybook(ctx context.Context, pb *LearnedPlaybook, symptom string, settings SentinelSettings) {
	s.mu.Lock()
	s.lastAction = fmt.Sprintf("playbook:%s->%s", pb.Trigger, pb.Action)
	s.mu.Unlock()

	if settings.AutonomyLevel == "safe_auto" && isSafeAutoAction(pb.Action) && s.actions != nil {
		err := s.actions.Apply(ctx, pb.Action, pb.Target)
		outcome := "success"
		if err != nil {
			outcome = "failed"
			s.mu.Lock()
			s.lastError = err.Error()
			s.mu.Unlock()
		} else {
			time.Sleep(1 * time.Second)
			ver, verErr := s.actions.Verify(ctx, pb.Action, pb.Target)
			if verErr != nil || (ver != nil && ver.Status == "failed") {
				outcome = "failed"
			} else {
				pb.SuccessCount++
				pb.LastUsedAt = time.Now()
				s.memory.AddOrUpdatePlaybook(*pb)
			}
		}

		s.memory.AddJournalEntry(LearningJournalEntry{
			Trigger:     symptom,
			Query:       fmt.Sprintf("Автономное устранение сбоя по сценарию %q", pb.Title),
			CloudAdvice: pb.Diagnosis,
			ActionTaken: fmt.Sprintf("%s(%s)", pb.Action, pb.Target),
			Outcome:     outcome,
		})
		return
	}

	// Notify only or requires user confirmation
	proposal := validatedRemediationProposal(pb.Action, pb.Target)
	if proposal != nil && s.service != nil {
		s.service.SetProposal(proposal)
	}

	s.memory.AddJournalEntry(LearningJournalEntry{
		Trigger:     symptom,
		Query:       fmt.Sprintf("Подготовлено предложение для пользователя по сценарию %q", pb.Title),
		CloudAdvice: pb.Diagnosis,
		ActionTaken: fmt.Sprintf("proposal: %s(%s)", pb.Action, pb.Target),
		Outcome:     "pending",
	})
}

func (s *Sentinel) consultCloudAI(ctx context.Context, symptom string, settings SentinelSettings) {
	if s.service == nil || s.service.config == nil || s.service.model == nil {
		return
	}
	cfg := s.service.config.Get()
	if !cfg.Enabled || (cfg.APIKey == "" && cfg.Provider != "local_embedded") {
		return
	}

	s.mu.Lock()
	lastHit, exists := s.recentCloudHits[symptom]
	if exists && time.Since(lastHit) < 15*time.Minute {
		s.mu.Unlock()
		return
	}
	s.recentCloudHits[symptom] = time.Now()
	s.mu.Unlock()

	consultCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	query := fmt.Sprintf(`Фоновый наблюдатель (Sentinel) роутера Keenetic обнаружил сетевой сбой: %s.
Определи диагноз и подбери действие из разрешённых: singbox.restart, mihomo.restart, mihomo.reload, routing.reapply, tunnel.restart, service.restart, dns.flush.
Ответь ТОЛЬКО валидным JSON без markdown-разметки:
{"title": "Краткое имя", "diagnosis": "В чём причина", "action": "одно из разрешённых", "target": "цель или пусто", "category": "routing/tunnel/dns"}`, symptom)

	ans, err := s.service.model.Analyze(consultCtx, cfg, query, []byte(`{"symptom":"`+symptom+`"}`))
	if err != nil {
		s.mu.Lock()
		s.lastError = "Cloud consultation: " + err.Error()
		s.mu.Unlock()
		return
	}

	type cloudResp struct {
		Title     string `json:"title"`
		Diagnosis string `json:"diagnosis"`
		Action    string `json:"action"`
		Target    string `json:"target"`
		Category  string `json:"category"`
	}

	cleanJSON := strings.TrimSpace(ans)
	if start := strings.Index(cleanJSON, "{"); start != -1 {
		if end := strings.LastIndex(cleanJSON, "}"); end != -1 && end > start {
			cleanJSON = cleanJSON[start : end+1]
		}
	}

	var parsed cloudResp
	if err := json.Unmarshal([]byte(cleanJSON), &parsed); err != nil {
		return
	}

	if parsed.Action == "" || parsed.Diagnosis == "" {
		return
	}
	if parsed.Title == "" {
		parsed.Title = symptom
	}
	if parsed.Category == "" {
		parsed.Category = "routing"
	}

	proposal := validatedRemediationProposal(parsed.Action, parsed.Target)
	if proposal == nil {
		return
	}

	newPB := s.memory.AddOrUpdatePlaybook(LearnedPlaybook{
		Category:    parsed.Category,
		Title:       parsed.Title,
		Trigger:     symptom,
		Diagnosis:   parsed.Diagnosis,
		Action:      parsed.Action,
		Target:      parsed.Target,
		LearnedFrom: "cloud_" + cfg.Provider,
	})

	s.memory.AddJournalEntry(LearningJournalEntry{
		Trigger:     symptom,
		Query:       "Запрос к облачному ИИ для обучения новому playbook",
		CloudAdvice: fmt.Sprintf("[%s] %s", parsed.Title, parsed.Diagnosis),
		ActionTaken: fmt.Sprintf("learned_playbook: %s(%s)", parsed.Action, parsed.Target),
		Outcome:     "learned",
	})

	if settings.AutonomyLevel == "safe_auto" && isSafeAutoAction(parsed.Action) && s.actions != nil {
		s.applyPlaybook(ctx, &newPB, symptom, settings)
	} else if s.service != nil {
		s.service.SetProposal(proposal)
	}
}

func isSafeAutoAction(action string) bool {
	switch action {
	case "singbox.restart", "mihomo.restart", "mihomo.reload", "tunnel.restart", "routing.reapply", "dns.flush":
		return true
	default:
		return false
	}
}
