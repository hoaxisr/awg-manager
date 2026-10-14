package aiassistant

import (
	"strings"
	"testing"
	"time"
)

func TestChangeProposalValidation(t *testing.T) {
	tests := []struct {
		name    string
		changes []Change
		wantErr bool
	}{
		{
			name:    "no changes",
			changes: []Change{},
			wantErr: true,
		},
		{
			name: "valid tunnel restart",
			changes: []Change{
				{Type: ChangeTunnelRestart, Target: "wg0", Title: "Restart wg0"},
			},
			wantErr: false,
		},
		{
			name: "invalid tunnel restart missing target",
			changes: []Change{
				{Type: ChangeTunnelRestart, Title: "Restart tunnel"},
			},
			wantErr: true,
		},
		{
			name: "valid engine switch",
			changes: []Change{
				{Type: ChangeEngineSwitch, Target: "sing-box"},
			},
			wantErr: false,
		},
		{
			name: "invalid engine switch",
			changes: []Change{
				{Type: ChangeEngineSwitch, Target: "invalid"},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cp := &ChangeProposal{Changes: tt.changes}
			err := cp.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestChangeProposalDiffPreview(t *testing.T) {
	cp := &ChangeProposal{
		Changes: []Change{
			{Type: ChangeTunnelRestart, Target: "wg0"},
			{Type: ChangeEngineSwitch, Target: "mihomo"},
		},
	}
	preview := cp.GenerateDiffPreview()
	if !strings.Contains(preview, "Restart tunnel: wg0") {
		t.Errorf("expected 'Restart tunnel: wg0' in preview, got: %s", preview)
	}
	if !strings.Contains(preview, "Switch engine to: mihomo") {
		t.Errorf("expected 'Switch engine to: mihomo' in preview, got: %s", preview)
	}
}

func TestChangeProposalExpiration(t *testing.T) {
	fresh := &ChangeProposal{CreatedAt: time.Now()}
	if fresh.IsExpired(10 * time.Minute) {
		t.Fatal("fresh proposal should not be expired")
	}

	stale := &ChangeProposal{CreatedAt: time.Now().Add(-11 * time.Minute)}
	if !stale.IsExpired(10 * time.Minute) {
		t.Fatal("stale proposal should be expired")
	}
}

func TestRemediationChangeConversions(t *testing.T) {
	rp := &RemediationProposal{
		ID:        "p1",
		Action:    "tunnel.restart",
		Target:    "awg1",
		Title:     "Restart awg1",
		Risk:      "low",
		Status:    "pending",
		CreatedAt: time.Now(),
	}

	cp := RemediationToChangeProposal(rp, "run-123")
	if cp == nil || cp.ID != "p1" || cp.RunID != "run-123" || len(cp.Changes) != 1 {
		t.Fatalf("unexpected cp: %+v", cp)
	}
	if cp.Changes[0].Type != ChangeTunnelRestart || cp.Changes[0].Target != "awg1" {
		t.Fatalf("unexpected change in cp: %+v", cp.Changes[0])
	}

	convertedBack := ChangeToRemediationProposal(cp)
	if convertedBack == nil || convertedBack.ID != "p1" || convertedBack.Action != "tunnel.restart" || convertedBack.Target != "awg1" {
		t.Fatalf("unexpected convertedBack: %+v", convertedBack)
	}
}
