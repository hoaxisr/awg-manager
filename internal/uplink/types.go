// Package uplink owns provider failover independently of any proxy engine.
package uplink

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrConflict    = errors.New("uplink policy revision conflict")
	ErrUnsupported = errors.New("uplink operation unsupported")
	ErrNotFound    = errors.New("uplink policy not found")
	// ErrPathUnavailable is allowed only before an adapter mutates runtime
	// or durable configuration. The controller can safely try another WAN.
	ErrPathUnavailable = errors.New("uplink path could not be prepared")
)

// Target keeps engine identifiers out of controller decisions. A policy ID is
// stable; remapping its target does not change provider preferences.
type Target struct {
	Engine string `json:"engine"`
	Ref    string `json:"ref"`
	Kind   string `json:"kind,omitempty"`
}

type Policy struct {
	Mode                    string   `json:"mode,omitempty"`
	Failback                string   `json:"failback,omitempty"`
	RecoveryThreshold       int      `json:"recoveryThreshold,omitempty"`
	RecoveryIntervalSeconds int      `json:"recoveryIntervalSeconds,omitempty"`
	ID                      string   `json:"id"`
	Revision                uint64   `json:"revision"`
	Target                  Target   `json:"target"`
	Providers               []string `json:"providers"` // stable NDMS IDs, in priority order
	Enabled                 bool     `json:"enabled"`
	IntervalSeconds         int      `json:"intervalSeconds"`
	TimeoutSeconds          int      `json:"timeoutSeconds"`
	FailureThreshold        int      `json:"failureThreshold"`
	HoldDownSeconds         int      `json:"holdDownSeconds"`
}

func (p *Policy) Defaults() {
	if p.Mode == "" {
		p.Mode = "fallback"
	}
	if p.Failback == "" {
		p.Failback = "manual"
	}
	if p.RecoveryThreshold == 0 {
		p.RecoveryThreshold = 3
	}
	if p.RecoveryIntervalSeconds == 0 {
		p.RecoveryIntervalSeconds = 30
	}
	if p.IntervalSeconds == 0 {
		p.IntervalSeconds = 10
	}
	if p.TimeoutSeconds == 0 {
		p.TimeoutSeconds = 4
	}
	if p.FailureThreshold == 0 {
		p.FailureThreshold = 3
	}
	if p.HoldDownSeconds == 0 {
		p.HoldDownSeconds = 60
	}
}

func (p Policy) Validate() error {
	if p.Mode != "" && p.Mode != "fallback" && p.Mode != "pinned" {
		return errors.New("unsupported uplink mode; automatic engine routing is configured outside managed policies")
	}
	if p.Failback != "" && p.Failback != "manual" && p.Failback != "automatic" && p.Failback != "controlled" {
		return errors.New("unsupported failback mode")
	}
	if p.Mode == "pinned" && (p.Failback == "automatic" || p.Failback == "controlled") {
		return errors.New("automatic return requires a fallback provider chain")
	}
	if p.RecoveryThreshold < 0 || p.RecoveryThreshold > 10 || p.RecoveryIntervalSeconds < 0 || p.RecoveryIntervalSeconds > 3600 || (p.RecoveryIntervalSeconds > 0 && p.RecoveryIntervalSeconds < p.TimeoutSeconds) {
		return errors.New("invalid recovery probe settings")
	}
	if p.Target.Kind != "" && p.Target.Kind != "subscription" {
		return errors.New("unsupported uplink target kind")
	}
	if strings.TrimSpace(p.ID) == "" || len(p.ID) > 128 || strings.TrimSpace(p.Target.Engine) == "" || strings.TrimSpace(p.Target.Ref) == "" {
		return errors.New("policy ID and engine target are required")
	}
	if (p.Mode == "pinned" && len(p.Providers) != 1) || (p.Mode != "pinned" && (len(p.Providers) < 2 || len(p.Providers) > 8)) {
		return errors.New("specify 2 to 8 providers")
	}
	seen := map[string]bool{}
	for _, id := range p.Providers {
		if strings.TrimSpace(id) != id || id == "" || seen[id] {
			return errors.New("provider IDs must be nonempty and unique")
		}
		seen[id] = true
	}
	if p.TimeoutSeconds < 1 || p.TimeoutSeconds > 30 || p.IntervalSeconds < p.TimeoutSeconds || p.IntervalSeconds > 3600 || p.FailureThreshold < 1 || p.FailureThreshold > 10 || p.HoldDownSeconds < 10 || p.HoldDownSeconds > 3600 {
		return errors.New("invalid probe interval, timeout, threshold or hold-down")
	}
	return nil
}

type Provider struct {
	ID     string `json:"id"`
	Device string `json:"device"`
	Label  string `json:"label"`
	Up     bool   `json:"up"`
}

type Resolver interface {
	Providers() []Provider
}

type Observation struct {
	Device      string
	Enabled     bool
	NeedsRebind bool // explicit source address is no longer assigned to this WAN
}

// Adapter must preserve strict binding: disappearance of a device MUST NOT
// downgrade to automatic routing. Probe must traverse the actual tunnel, not a
// direct socket to its server. An apply error leaves actual state unknown.
// First release deliberately uses controlled activation and manual failback.
type Adapter interface {
	Validate(context.Context, Target) error
	Observe(context.Context, Target) (Observation, error)
	Activate(context.Context, Target, string, string) error // expected device, next device
	Probe(context.Context, Target, string) error
}

// CandidateProber MUST leave the live tunnel and its config unchanged. Any
// temporary listeners/processes must be closed before returning, also on cancel.
type CandidateProber interface {
	ProbeCandidate(context.Context, Target, string) error
}
type Capabilities struct {
	ProviderDNS        bool   `json:"providerDNS,omitempty"`
	IndependentEngine  bool   `json:"independentEngine,omitempty"`
	IndependentProbe   bool   `json:"independentProbe"`
	AutomaticFailback  bool   `json:"automaticFailback"`
	ControlledFailback bool   `json:"controlledFailback,omitempty"`
	RestartScope       string `json:"restartScope"`
	Limitation         string `json:"limitation,omitempty"`
}
type CapabilityProvider interface{ UplinkCapabilities(Target) Capabilities }

type TargetInfo struct {
	Target            Target `json:"target"`
	Label             string `json:"label"`
	UnavailableReason string `json:"unavailableReason,omitempty"`
	SetupRequired     bool   `json:"setupRequired,omitempty"`
}

// TargetPreparer creates narrowly scoped prerequisites only when an enabled
// policy is explicitly saved. Inventory and background probes remain read-only.
type TargetPreparer interface {
	PrepareUplink(context.Context, Target) error
}
type TargetLister interface {
	UplinkTargets(context.Context) ([]TargetInfo, error)
}
type Releaser interface {
	ReleaseUplink(context.Context, Target) error
}

type State struct {
	PolicyRevision    uint64    `json:"policyRevision"`
	Generation        uint64    `json:"generation"`
	OperationID       string    `json:"operationId,omitempty"`
	RecoverySuccesses int       `json:"recoverySuccesses,omitempty"`
	Candidate         string    `json:"candidate,omitempty"`
	NextRecoveryCheck time.Time `json:"nextRecoveryCheck,omitempty"`
	Phase             string    `json:"phase"`
	Provider          string    `json:"provider,omitempty"`
	Device            string    `json:"device,omitempty"`
	Failures          int       `json:"failures"`
	LastSuccess       time.Time `json:"lastSuccess,omitempty"`
	LastSwitch        time.Time `json:"lastSwitch,omitempty"`
	LastError         string    `json:"lastError,omitempty"`
	NextCheck         time.Time `json:"nextCheck,omitempty"`
}

type Entry struct {
	Policy       Policy       `json:"policy"`
	State        State        `json:"state"`
	Capabilities Capabilities `json:"capabilities"`
}

func providerFor(list []Provider, id string) (Provider, error) {
	for _, p := range list {
		if p.ID == id && p.Device != "" {
			return p, nil
		}
	}
	return Provider{}, fmt.Errorf("provider %q is not present", id)
}
