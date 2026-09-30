package aiassistant

import (
	"fmt"
	"strings"
	"time"
)

type ChangeType string

const (
	ChangeTunnelRestart      ChangeType = "tunnel_restart"
	ChangeSingboxRestart     ChangeType = "singbox_restart"
	ChangeMihomoReload       ChangeType = "mihomo_reload"
	ChangeEngineSwitch       ChangeType = "engine_switch"
	ChangeDNSFlush           ChangeType = "dns_flush"
	ChangeSubscriptionUpdate ChangeType = "subscription_update"
	ChangeServiceRestart     ChangeType = "service_restart"
	ChangeIPTablesRestore    ChangeType = "iptables_restore"
	ChangeConfigPatch        ChangeType = "config_patch"
)

type Change struct {
	Type    ChangeType `json:"type"`
	Target  string     `json:"target"`
	Payload string     `json:"payload,omitempty"`
	Title   string     `json:"title"`
}

type ChangeProposal struct {
	ID          string     `json:"id"`
	RunID       string     `json:"runId"`
	Revision    uint64     `json:"revision"`
	Goal        string     `json:"goal"`
	Changes     []Change   `json:"changes"`
	DiffPreview string     `json:"diffPreview"`
	Risk        string     `json:"risk"`
	Status      string     `json:"status"` // pending|approved|applied|failed|rolled_back
	CreatedAt   time.Time  `json:"createdAt"`
	AppliedAt   *time.Time `json:"appliedAt,omitempty"`
	Error       string     `json:"error,omitempty"`
}

func (c *Change) validate() error {
	switch c.Type {
	case ChangeTunnelRestart, ChangeSubscriptionUpdate, ChangeServiceRestart:
		if c.Target == "" {
			return fmt.Errorf("target is required for change type %s", c.Type)
		}
	case ChangeEngineSwitch:
		if c.Target != "mihomo" && c.Target != "sing-box" {
			return fmt.Errorf("invalid engine target: %s", c.Target)
		}
	}
	return nil
}

func (cp *ChangeProposal) Validate() error {
	if len(cp.Changes) == 0 {
		return fmt.Errorf("proposal must contain at least one change")
	}
	for i, c := range cp.Changes {
		if err := c.validate(); err != nil {
			return fmt.Errorf("invalid change at index %d: %w", i, err)
		}
	}
	return nil
}

func (cp *ChangeProposal) GenerateDiffPreview() string {
	var sb strings.Builder
	for _, c := range cp.Changes {
		switch c.Type {
		case ChangeTunnelRestart:
			sb.WriteString(fmt.Sprintf("- Restart tunnel: %s\n", c.Target))
		case ChangeSingboxRestart:
			sb.WriteString("- Restart sing-box\n")
		case ChangeMihomoReload:
			sb.WriteString("- Reload mihomo config\n")
		case ChangeEngineSwitch:
			sb.WriteString(fmt.Sprintf("- Switch engine to: %s\n", c.Target))
		case ChangeDNSFlush:
			sb.WriteString("- Flush DNS cache\n")
		case ChangeSubscriptionUpdate:
			sb.WriteString(fmt.Sprintf("- Update subscription: %s\n", c.Target))
		case ChangeServiceRestart:
			sb.WriteString(fmt.Sprintf("- Restart service: %s\n", c.Target))
		case ChangeIPTablesRestore:
			sb.WriteString("- Restore iptables from backup\n")
		case ChangeConfigPatch:
			sb.WriteString(fmt.Sprintf("- Patch config %s\n", c.Target))
		default:
			sb.WriteString(fmt.Sprintf("- %s: %s\n", c.Type, c.Title))
		}
	}
	return sb.String()
}

func (cp *ChangeProposal) IsExpired(ttl time.Duration) bool {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return time.Since(cp.CreatedAt) > ttl
}

func RemediationToChangeProposal(rp *RemediationProposal, runID string) *ChangeProposal {
	if rp == nil {
		return nil
	}
	var cType ChangeType
	switch rp.Action {
	case "tunnel.restart":
		cType = ChangeTunnelRestart
	case "singbox.restart":
		cType = ChangeSingboxRestart
	case "mihomo.reload":
		cType = ChangeMihomoReload
	case "mihomo.restart":
		cType = ChangeMihomoReload
	case "routing.switch_engine":
		cType = ChangeEngineSwitch
	case "dns.flush":
		cType = ChangeDNSFlush
	case "subscription.update":
		cType = ChangeSubscriptionUpdate
	case "service.restart", "service.start", "service.stop":
		cType = ChangeServiceRestart
	case "iptables.restore":
		cType = ChangeIPTablesRestore
	default:
		cType = ChangeType(rp.Action)
	}

	cp := &ChangeProposal{
		ID:        rp.ID,
		RunID:     runID,
		Goal:      rp.Title,
		Risk:      rp.Risk,
		Status:    rp.Status,
		CreatedAt: rp.CreatedAt,
		Error:     rp.Error,
		Changes: []Change{
			{
				Type:   cType,
				Target: rp.Target,
				Title:  rp.Title,
			},
		},
	}
	cp.DiffPreview = cp.GenerateDiffPreview()
	return cp
}

func ChangeToRemediationProposal(cp *ChangeProposal) *RemediationProposal {
	if cp == nil || len(cp.Changes) == 0 {
		return nil
	}
	firstChange := cp.Changes[0]
	action := string(firstChange.Type)
	switch firstChange.Type {
	case ChangeTunnelRestart:
		action = "tunnel.restart"
	case ChangeSingboxRestart:
		action = "singbox.restart"
	case ChangeMihomoReload:
		action = "mihomo.reload"
	case ChangeEngineSwitch:
		action = "routing.switch_engine"
	case ChangeDNSFlush:
		action = "dns.flush"
	case ChangeSubscriptionUpdate:
		action = "subscription.update"
	case ChangeServiceRestart:
		action = "service.restart"
	case ChangeIPTablesRestore:
		action = "iptables.restore"
	}

	return &RemediationProposal{
		ID:          cp.ID,
		Action:      action,
		Target:      firstChange.Target,
		Title:       cp.Goal,
		Description: cp.DiffPreview,
		Risk:        cp.Risk,
		Status:      cp.Status,
		CreatedAt:   cp.CreatedAt,
		Error:       cp.Error,
	}
}
