package model

import "time"

type CandidateState string

const (
	CandidateChecking CandidateState = "checking"
	CandidateWaiting  CandidateState = "waiting"
	CandidateAdded    CandidateState = "added"
	CandidateRejected CandidateState = "rejected"
)

type CheckEvidence struct {
	Verdict    Verdict   `json:"verdict"`
	Bytes      int64     `json:"bytes,omitempty"`
	DurationMS int64     `json:"durationMs,omitempty"`
	Detail     string    `json:"detail,omitempty"`
	CheckedAt  time.Time `json:"checkedAt,omitempty"`
}

type CoverageEvidence struct {
	Covered    bool   `json:"covered,omitempty"`
	Excluded   bool   `json:"excluded,omitempty"`
	Detail     string `json:"detail,omitempty"`
	DurationMS int64  `json:"durationMs,omitempty"`
}

// Candidate contains all evidence for one decision. For IP-only targets Target
// describes the latest triggering endpoint while ID covers the whole address.
type Candidate struct {
	ID              string           `json:"id"`
	Target          Target           `json:"target"`
	Reason          string           `json:"reason"`
	ClientIP        string           `json:"clientIP,omitempty"`
	ClientMAC       string           `json:"clientMAC,omitempty"`
	State           CandidateState   `json:"state"`
	FirstAt         time.Time        `json:"firstAt"`
	LastAt          time.Time        `json:"lastAt"`
	Coverage        CoverageEvidence `json:"coverage"`
	Direct          CheckEvidence    `json:"direct"`
	WAN             CheckEvidence    `json:"wan"`
	Tunnel          CheckEvidence    `json:"tunnel"`
	TargetInterface string           `json:"targetInterface,omitempty"`
	TargetOutbound  string           `json:"targetOutbound,omitempty"`
	CorrelationID   string           `json:"correlationID"`
	LastError       string           `json:"lastError,omitempty"`
}

type HistoryEntry struct {
	At        time.Time `json:"at"`
	Domain    string    `json:"domain,omitempty"`
	Host      string    `json:"host,omitempty"`
	IP        string    `json:"ip,omitempty"`
	Network   Network   `json:"network,omitempty"`
	Port      uint16    `json:"port,omitempty"`
	Action    string    `json:"action"`
	Source    string    `json:"source"`
	Interface string    `json:"interface,omitempty"`
	Outbound  string    `json:"outbound,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	Why       string    `json:"why,omitempty"` // legacy history field
	Result    string    `json:"result,omitempty"`
}
