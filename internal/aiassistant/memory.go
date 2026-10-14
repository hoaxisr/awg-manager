package aiassistant

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// MemoryFact represents a learned fact about the local network, topology, or environment.
type MemoryFact struct {
	ID        string    `json:"id"`
	Category  string    `json:"category"` // "network", "hardware", "isp", "service", "preference"
	Content   string    `json:"content"`  // Human-readable fact
	Source    string    `json:"source"`   // "user", "cloud", "system"
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// LearnedPlaybook represents a proven remedy/solution learned from cloud consultations or user guidance.
type LearnedPlaybook struct {
	ID           string    `json:"id"`
	Category     string    `json:"category"`  // "routing", "cloud", "dns", "tunnel"
	Title        string    `json:"title"`     // Short name of the playbook
	Trigger      string    `json:"trigger"`   // Symptom or error signature (e.g. "mws_cloud_timeout", "keendns_relay_blocked")
	Diagnosis    string    `json:"diagnosis"` // Explanation of the cause
	Action       string    `json:"action"`    // Remediation action (e.g. "reapply_routing", "restart_singbox", "add_cloud_ip")
	Target       string    `json:"target,omitempty"`
	SuccessCount int       `json:"successCount"`
	LearnedFrom  string    `json:"learnedFrom"` // "cloud_gemini", "cloud_claude", "user"
	CreatedAt    time.Time `json:"createdAt"`
	LastUsedAt   time.Time `json:"lastUsedAt,omitempty"`
}

// LearningJournalEntry tracks interactions between the local sentinel and cloud agent.
type LearningJournalEntry struct {
	ID          string    `json:"id"`
	Timestamp   time.Time `json:"timestamp"`
	Trigger     string    `json:"trigger"`
	Query       string    `json:"query"`
	CloudAdvice string    `json:"cloudAdvice"`
	ActionTaken string    `json:"actionTaken"`
	Outcome     string    `json:"outcome"` // "success", "failed", "pending"
}

// SentinelSettings configures the autonomous background sentinel.
type SentinelSettings struct {
	Enabled         bool   `json:"enabled"`
	IntervalSeconds int    `json:"intervalSeconds"` // default 60
	AutonomyLevel   string `json:"autonomyLevel"`   // "notify_only", "safe_auto", "disabled"
}

// AIMemoryData is the root JSON structure serialized to ai-memory.json.
type AIMemoryData struct {
	Facts     []MemoryFact           `json:"facts"`
	Playbooks []LearnedPlaybook      `json:"playbooks"`
	Journal   []LearningJournalEntry `json:"journal"`
	Settings  SentinelSettings       `json:"settings"`
	UpdatedAt time.Time              `json:"updatedAt"`
}

func (f *MemoryFact) UnmarshalJSON(data []byte) error {
	type Alias MemoryFact
	aux := struct {
		*Alias
		CreatedAtSnake *time.Time `json:"created_at"`
		UpdatedAtSnake *time.Time `json:"updated_at"`
	}{
		Alias: (*Alias)(f),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if f.CreatedAt.IsZero() && aux.CreatedAtSnake != nil {
		f.CreatedAt = *aux.CreatedAtSnake
	}
	if f.UpdatedAt.IsZero() && aux.UpdatedAtSnake != nil {
		f.UpdatedAt = *aux.UpdatedAtSnake
	}
	return nil
}

func (p *LearnedPlaybook) UnmarshalJSON(data []byte) error {
	type Alias LearnedPlaybook
	aux := struct {
		*Alias
		SuccessCountSnake int        `json:"success_count"`
		LearnedFromSnake  string     `json:"learned_from"`
		CreatedAtSnake    *time.Time `json:"created_at"`
		LastUsedAtSnake   *time.Time `json:"last_used_at"`
	}{
		Alias: (*Alias)(p),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if p.SuccessCount == 0 && aux.SuccessCountSnake > 0 {
		p.SuccessCount = aux.SuccessCountSnake
	}
	if p.LearnedFrom == "" && aux.LearnedFromSnake != "" {
		p.LearnedFrom = aux.LearnedFromSnake
	}
	if p.CreatedAt.IsZero() && aux.CreatedAtSnake != nil {
		p.CreatedAt = *aux.CreatedAtSnake
	}
	if p.LastUsedAt.IsZero() && aux.LastUsedAtSnake != nil {
		p.LastUsedAt = *aux.LastUsedAtSnake
	}
	if p.Title == "" {
		if p.Trigger != "" {
			p.Title = p.Trigger
		} else if p.Action != "" {
			p.Title = p.Action
		}
	}
	return nil
}

func (j *LearningJournalEntry) UnmarshalJSON(data []byte) error {
	type Alias LearningJournalEntry
	aux := struct {
		*Alias
		Diagnosis   string `json:"diagnosis"`
		Action      string `json:"action"`
		ActionSnake string `json:"action_taken"`
		CloudSnake  string `json:"cloud_advice"`
	}{
		Alias: (*Alias)(j),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if j.CloudAdvice == "" {
		if aux.CloudSnake != "" {
			j.CloudAdvice = aux.CloudSnake
		} else if aux.Diagnosis != "" {
			j.CloudAdvice = aux.Diagnosis
		}
	}
	if j.ActionTaken == "" {
		if aux.ActionSnake != "" {
			j.ActionTaken = aux.ActionSnake
		} else if aux.Action != "" {
			j.ActionTaken = aux.Action
		}
	}
	if j.ID == "" {
		j.ID = genMemoryID()
	}
	return nil
}

func (d *AIMemoryData) UnmarshalJSON(data []byte) error {
	type Alias AIMemoryData
	aux := struct {
		*Alias
		LearningJournal []LearningJournalEntry `json:"learning_journal"`
		UpdatedAtSnake  *time.Time             `json:"updated_at"`
	}{
		Alias: (*Alias)(d),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if len(d.Journal) == 0 && len(aux.LearningJournal) > 0 {
		d.Journal = aux.LearningJournal
	}
	if d.UpdatedAt.IsZero() && aux.UpdatedAtSnake != nil {
		d.UpdatedAt = *aux.UpdatedAtSnake
	}
	return nil
}

func defaultSentinelSettings() SentinelSettings {
	return SentinelSettings{
		Enabled:         true,
		IntervalSeconds: 60,
		AutonomyLevel:   "notify_only",
	}
}

// MemoryStore manages persistent facts, playbooks, and sentinel settings.
type MemoryStore struct {
	path string
	mu   sync.RWMutex
	data AIMemoryData
}

func NewMemoryStore(filePath string) (*MemoryStore, error) {
	s := &MemoryStore{
		path: filePath,
		data: AIMemoryData{
			Facts:     []MemoryFact{},
			Playbooks: []LearnedPlaybook{},
			Journal:   []LearningJournalEntry{},
			Settings:  defaultSentinelSettings(),
			UpdatedAt: time.Now(),
		},
	}
	if filePath == "" {
		return s, nil
	}
	b, err := os.ReadFile(filePath)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("AI memory: read: %w", err)
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		return nil, fmt.Errorf("AI memory: unmarshal: %w", err)
	}
	if s.data.Settings.AutonomyLevel == "" {
		s.data.Settings = defaultSentinelSettings()
	}
	return s, nil
}

func genMemoryID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// AddFact records a new fact or updates an existing identical content entry.
func (s *MemoryStore) AddFact(category, content, source string) MemoryFact {
	s.mu.Lock()
	defer s.mu.Unlock()

	content = strings.TrimSpace(content)
	category = strings.TrimSpace(category)
	if category == "" {
		category = "general"
	}
	if source == "" {
		source = "user"
	}

	for i, f := range s.data.Facts {
		if strings.EqualFold(f.Content, content) {
			s.data.Facts[i].UpdatedAt = time.Now()
			s.data.Facts[i].Category = category
			_ = s.saveLocked()
			return s.data.Facts[i]
		}
	}

	fact := MemoryFact{
		ID:        genMemoryID(),
		Category:  category,
		Content:   content,
		Source:    source,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	s.data.Facts = append(s.data.Facts, fact)
	_ = s.saveLocked()
	return fact
}

// RemoveFact deletes a fact by ID.
func (s *MemoryStore) RemoveFact(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, f := range s.data.Facts {
		if f.ID == id {
			s.data.Facts = append(s.data.Facts[:i], s.data.Facts[i+1:]...)
			_ = s.saveLocked()
			return true
		}
	}
	return false
}

// ListFacts returns a copy of all facts, optionally filtered by category.
func (s *MemoryStore) ListFacts(category string) []MemoryFact {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if category == "" {
		return slices.Clone(s.data.Facts)
	}
	var out []MemoryFact
	for _, f := range s.data.Facts {
		if strings.EqualFold(f.Category, category) {
			out = append(out, f)
		}
	}
	return out
}

// AddOrUpdatePlaybook registers a learned problem resolution.
func (s *MemoryStore) AddOrUpdatePlaybook(pb LearnedPlaybook) LearnedPlaybook {
	s.mu.Lock()
	defer s.mu.Unlock()

	pb.Trigger = strings.TrimSpace(pb.Trigger)
	for i, existing := range s.data.Playbooks {
		if strings.EqualFold(existing.Trigger, pb.Trigger) && strings.EqualFold(existing.Action, pb.Action) {
			s.data.Playbooks[i].SuccessCount++
			s.data.Playbooks[i].LastUsedAt = time.Now()
			if pb.Diagnosis != "" {
				s.data.Playbooks[i].Diagnosis = pb.Diagnosis
			}
			_ = s.saveLocked()
			return s.data.Playbooks[i]
		}
	}

	if pb.ID == "" {
		pb.ID = genMemoryID()
	}
	if pb.SuccessCount == 0 {
		pb.SuccessCount = 1
	}
	pb.CreatedAt = time.Now()
	pb.LastUsedAt = time.Now()
	s.data.Playbooks = append(s.data.Playbooks, pb)
	_ = s.saveLocked()
	return pb
}

// RemovePlaybook removes a playbook by ID.
func (s *MemoryStore) RemovePlaybook(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, pb := range s.data.Playbooks {
		if pb.ID == id {
			s.data.Playbooks = append(s.data.Playbooks[:i], s.data.Playbooks[i+1:]...)
			_ = s.saveLocked()
			return true
		}
	}
	return false
}

// ListPlaybooks returns all learned playbooks.
func (s *MemoryStore) ListPlaybooks() []LearnedPlaybook {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.data.Playbooks)
}

// FindPlaybook searches for a playbook matching the given symptom/trigger.
func (s *MemoryStore) FindPlaybook(symptom string) *LearnedPlaybook {
	s.mu.RLock()
	defer s.mu.RUnlock()

	lowSym := strings.ToLower(symptom)
	for _, pb := range s.data.Playbooks {
		lowTrig := strings.ToLower(pb.Trigger)
		if strings.Contains(lowSym, lowTrig) || strings.Contains(lowTrig, lowSym) {
			cp := pb
			return &cp
		}
		// Match by individual tokens
		parts := strings.FieldsFunc(lowTrig, func(r rune) bool {
			return r == '_' || r == '-' || r == ' ' || r == '.'
		})
		matched := 0
		for _, p := range parts {
			if len(p) >= 3 && strings.Contains(lowSym, p) {
				matched++
			}
		}
		if len(parts) > 0 && float64(matched)/float64(len(parts)) >= 0.5 {
			cp := pb
			return &cp
		}
	}
	return nil
}

// AddJournalEntry appends an interaction to the learning journal (capped at 100 entries).
func (s *MemoryStore) AddJournalEntry(e LearningJournalEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if e.ID == "" {
		e.ID = genMemoryID()
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
	s.data.Journal = append([]LearningJournalEntry{e}, s.data.Journal...)
	if len(s.data.Journal) > 100 {
		s.data.Journal = s.data.Journal[:100]
	}
	_ = s.saveLocked()
}

// ListJournal returns the learning journal entries.
func (s *MemoryStore) ListJournal(limit int) []LearningJournalEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 || limit > len(s.data.Journal) {
		limit = len(s.data.Journal)
	}
	return slices.Clone(s.data.Journal[:limit])
}

// Settings returns the current sentinel settings.
func (s *MemoryStore) Settings() SentinelSettings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.Settings
}

// UpdateSettings modifies the sentinel settings.
func (s *MemoryStore) UpdateSettings(cfg SentinelSettings) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if cfg.IntervalSeconds < 10 {
		cfg.IntervalSeconds = 60
	}
	if cfg.AutonomyLevel == "" {
		cfg.AutonomyLevel = "notify_only"
	}
	s.data.Settings = cfg
	return s.saveLocked()
}

// RenderPromptContext formats learned facts and playbooks as a system prompt block.
func (s *MemoryStore) RenderPromptContext() string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.data.Facts) == 0 && len(s.data.Playbooks) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("\n\n### ДОЛГОВРЕМЕННАЯ ПАМЯТЬ И ВЫУЧЕННЫЙ ОПЫТ ЭТОЙ СЕТИ:\n")

	if len(s.data.Facts) > 0 {
		b.WriteString("Факты о роутере и сети (запомненные ранее):\n")
		for _, f := range s.data.Facts {
			fmt.Fprintf(&b, "- [%s] %s\n", f.Category, f.Content)
		}
	}

	if len(s.data.Playbooks) > 0 {
		b.WriteString("\nПроверенные рецепты решения проблем (выученные решения):\n")
		for _, pb := range s.data.Playbooks {
			fmt.Fprintf(&b, "- При симптоме %q: %s (действие: %s, успешно применено %d раз)\n",
				pb.Trigger, pb.Diagnosis, pb.Action, pb.SuccessCount)
		}
	}

	b.WriteString("Учитывай эти факты и выученные рецепты в своих рассуждениях. Если пользователь просит что-то запомнить, вызови инструмент `memory.learn_fact`.\n")
	return b.String()
}

func (s *MemoryStore) saveLocked() error {
	if s.path == "" {
		return nil
	}
	s.data.UpdatedAt = time.Now()
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal AI memory: %w", err)
	}
	return storage.AtomicWritePerm(s.path, raw, 0644)
}
