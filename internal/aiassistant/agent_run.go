package aiassistant

import (
	"fmt"
	"time"
)

type AgentRunStatus string

const (
	AgentRunPending    AgentRunStatus = "pending"
	AgentRunInProgress AgentRunStatus = "in_progress"
	AgentRunSuccess    AgentRunStatus = "success"
	AgentRunFailed     AgentRunStatus = "failed"
)

type ValidationResult struct {
	Passed bool   `json:"passed"`
	Error  string `json:"error,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type JournalEntry struct {
	Time     time.Time `json:"time"`
	Phase    string    `json:"phase"` // diagnose|propose|apply|verify|rollback|retry
	Message  string    `json:"message"`
	Error    string    `json:"error,omitempty"`
	Rollback string    `json:"rollback,omitempty"`
}

type AgentRun struct {
	ID        string            `json:"id"`
	StartedAt time.Time         `json:"startedAt"`
	UpdatedAt time.Time         `json:"updatedAt"`
	Status    AgentRunStatus    `json:"status"`
	Issue     string            `json:"issue"`
	Iteration int               `json:"iteration"`
	MaxIter   int               `json:"maxIter"`
	Proposals []*ChangeProposal `json:"proposals"`
	DiagLog   []string          `json:"diagLog"`
	Journal   []JournalEntry    `json:"journal"`
	Result    string            `json:"result,omitempty"`
}

func NewAgentRun(issue string) *AgentRun {
	now := time.Now()
	// Use time hash for simple unique ID as this runs locally
	id := fmt.Sprintf("run_%d", now.UnixMilli())
	return &AgentRun{
		ID:        id,
		StartedAt: now,
		UpdatedAt: now,
		Status:    AgentRunPending,
		Issue:     issue,
		Iteration: 1,
		MaxIter:   5, // default maximum retries
		Proposals: make([]*ChangeProposal, 0),
		DiagLog:   make([]string, 0),
		Journal:   make([]JournalEntry, 0),
	}
}

func (r *AgentRun) AddLog(msg string) {
	entry := fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), msg)
	r.DiagLog = append(r.DiagLog, entry)
	r.UpdatedAt = time.Now()
}

func (r *AgentRun) AddJournal(phase, message, errStr, rollbackStatus string) {
	entry := JournalEntry{
		Time:     time.Now(),
		Phase:    phase,
		Message:  message,
		Error:    errStr,
		Rollback: rollbackStatus,
	}
	r.Journal = append(r.Journal, entry)
	r.AddLog(fmt.Sprintf("[%s] %s", phase, message))
}

func (r *AgentRun) AddProposal(p *ChangeProposal) {
	p.RunID = r.ID
	r.Proposals = append(r.Proposals, p)
	r.UpdatedAt = time.Now()
}

func (r *AgentRun) GetLatestProposal() *ChangeProposal {
	if len(r.Proposals) == 0 {
		return nil
	}
	return r.Proposals[len(r.Proposals)-1]
}

func (r *AgentRun) Complete(success bool, msg string) {
	r.UpdatedAt = time.Now()
	r.Result = msg
	if success {
		r.Status = AgentRunSuccess
	} else {
		r.Status = AgentRunFailed
	}
}

func (r *AgentRun) CanRetry() bool {
	return r.Iteration < r.MaxIter
}

func (r *AgentRun) NextIteration() {
	r.Iteration++
	r.UpdatedAt = time.Now()
}

// Ensure interface compatibility for persistence layer
func (r *AgentRun) Clone() *AgentRun {
	// shallow copy core fields
	cloned := *r
	// deep copy slices
	cloned.Proposals = make([]*ChangeProposal, len(r.Proposals))
	for i, p := range r.Proposals {
		pc := *p
		if p.AppliedAt != nil {
			pt := *p.AppliedAt
			pc.AppliedAt = &pt
		}
		cloned.Proposals[i] = &pc
	}
	cloned.DiagLog = make([]string, len(r.DiagLog))
	copy(cloned.DiagLog, r.DiagLog)
	cloned.Journal = make([]JournalEntry, len(r.Journal))
	copy(cloned.Journal, r.Journal)
	return &cloned
}
