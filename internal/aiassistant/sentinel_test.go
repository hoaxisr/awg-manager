package aiassistant

import (
	"context"
	"testing"
)

type mockActionExecutor struct {
	appliedAction string
	appliedTarget string
}

func (m *mockActionExecutor) Apply(_ context.Context, action, target string) error {
	m.appliedAction = action
	m.appliedTarget = target
	return nil
}

func (m *mockActionExecutor) Verify(_ context.Context, action, target string) (*ActionVerification, error) {
	return &ActionVerification{Status: "passed", Summary: "Mock verified"}, nil
}

func TestSentinel_SafeAutoPlaybook(t *testing.T) {
	memStore, err := NewMemoryStore(t.TempDir() + "/test-sentinel.json")
	if err != nil {
		t.Fatalf("create memory store: %v", err)
	}
	memStore.UpdateSettings(SentinelSettings{
		Enabled:         true,
		IntervalSeconds: 10,
		AutonomyLevel:   "safe_auto",
	})

	// Pre-seed a playbook
	memStore.AddOrUpdatePlaybook(LearnedPlaybook{
		Category:    "tunnel",
		Title:       "Restart failing tunnel",
		Trigger:     "tunnel_down: opkgtun10",
		Diagnosis:   "Tunnel handshake dead",
		Action:      "tunnel.restart",
		Target:      "opkgtun10",
		LearnedFrom: "test",
	})

	svc := NewService(nil)
	actions := &mockActionExecutor{}
	sentinel := NewSentinel(svc, memStore, ToolSources{}, actions)

	// Invoke handleSymptom
	sentinel.handleSymptom(context.Background(), "tunnel_down: opkgtun10", memStore.Settings())

	if actions.appliedAction != "tunnel.restart" || actions.appliedTarget != "opkgtun10" {
		t.Fatalf("expected tunnel.restart on opkgtun10, got action=%s target=%s", actions.appliedAction, actions.appliedTarget)
	}

	// Verify journal
	journal := memStore.ListJournal(10)
	if len(journal) == 0 {
		t.Fatalf("expected journal entry recorded")
	}
	if journal[0].Outcome != "success" {
		t.Fatalf("expected outcome success, got %s", journal[0].Outcome)
	}
}

func TestSentinel_NotifyOnlyPlaybook(t *testing.T) {
	memStore, err := NewMemoryStore(t.TempDir() + "/test-sentinel-notify.json")
	if err != nil {
		t.Fatalf("create memory store: %v", err)
	}
	memStore.UpdateSettings(SentinelSettings{
		Enabled:         true,
		IntervalSeconds: 10,
		AutonomyLevel:   "notify_only",
	})

	memStore.AddOrUpdatePlaybook(LearnedPlaybook{
		Category:    "routing",
		Title:       "Reload mihomo config",
		Trigger:     "engine_stopped: mihomo",
		Diagnosis:   "Mihomo crashed",
		Action:      "mihomo.restart",
		Target:      "",
		LearnedFrom: "test",
	})

	svc := NewService(nil)
	sentinel := NewSentinel(svc, memStore, ToolSources{}, nil)

	sentinel.handleSymptom(context.Background(), "engine_stopped: mihomo", memStore.Settings())

	proposal := svc.Proposal()
	if proposal == nil {
		t.Fatalf("expected proposal created in notify_only mode")
	}
	if proposal.Action != "mihomo.restart" {
		t.Fatalf("expected mihomo.restart proposal, got %s", proposal.Action)
	}
}

func TestSentinel_ProbeHealth_StructuredEngineStatus(t *testing.T) {
	sources := ToolSources{
		EngineStatus: func(ctx context.Context) (any, error) {
			return map[string]any{
				"routing": map[string]any{
					"enabled":        true,
					"selectedEngine": "mihomo",
				},
				"mihomo": map[string]any{
					"running":  false,
					"selected": true,
					"enabled":  true,
				},
			}, nil
		},
	}

	sentinel := NewSentinel(nil, nil, sources, nil)
	symptom := sentinel.probeHealth(context.Background())
	if symptom != "engine_stopped: mihomo is stopped" {
		t.Fatalf("expected 'engine_stopped: mihomo is stopped', got %q", symptom)
	}

	// Now test when running
	sourcesRunning := ToolSources{
		DNSProbe: func(context.Context) error { return nil },
		EngineStatus: func(ctx context.Context) (any, error) {
			return map[string]any{
				"routing": map[string]any{
					"enabled":        true,
					"selectedEngine": "mihomo",
				},
				"mihomo": map[string]any{
					"running":  true,
					"selected": true,
					"enabled":  true,
				},
			}, nil
		},
	}
	sentinelRunning := NewSentinel(nil, nil, sourcesRunning, nil)
	symptomOk := sentinelRunning.probeHealth(context.Background())
	if symptomOk != "" {
		t.Fatalf("expected empty symptom when running, got %q", symptomOk)
	}
}

func TestSentinel_ProbeHealth_TunnelUsesInternalID(t *testing.T) {
	sources := ToolSources{
		DNSProbe: func(context.Context) error { return nil },
		Tunnels: func(ctx context.Context) (any, error) {
			return []map[string]any{
				{
					"id":      "awg-internal-id-123",
					"name":    "My Beautiful VPN Label",
					"enabled": true,
					"state":   "down",
				},
			}, nil
		},
	}
	sentinel := NewSentinel(nil, nil, sources, nil)
	symptom := sentinel.probeHealth(context.Background())
	if symptom != "tunnel_down: awg-internal-id-123" {
		t.Fatalf("expected 'tunnel_down: awg-internal-id-123', got %q", symptom)
	}
}

