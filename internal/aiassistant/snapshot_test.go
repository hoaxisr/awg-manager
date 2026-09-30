package aiassistant

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSnapshotManager(t *testing.T) {
	var executedCmds []string
	manager := NewSnapshotManager(
		func(ctx context.Context, cmd string) (string, error) { // execCmd
			executedCmds = append(executedCmds, cmd)
			return "ok", nil
		},
		func(ctx context.Context) (string, error) { // iptCmd
			return "*filter\nCOMMIT", nil
		},
		func(ctx context.Context) (string, error) { // rtCmd
			return "ip rule", nil
		},
		func(ctx context.Context) (map[string]string, error) { // srvCmd
			return map[string]string{"sing-box": "running"}, nil
		},
	)

	snap, err := manager.TakeSnapshot(context.Background())
	if err != nil {
		t.Fatalf("TakeSnapshot failed: %v", err)
	}

	if snap.IPTables != "*filter\nCOMMIT" {
		t.Errorf("wrong iptables dump: %s", snap.IPTables)
	}

	if snap.Services["sing-box"] != "running" {
		t.Errorf("wrong service state")
	}

	err = manager.RestoreSnapshot(context.Background(), snap)
	if err != nil {
		t.Fatalf("RestoreSnapshot failed: %v", err)
	}

	if len(executedCmds) < 2 {
		t.Errorf("expected restore commands to be executed, got: %v", executedCmds)
	}
}

func TestNoopSnapshotManager(t *testing.T) {
	noop := NewNoopSnapshotManager()
	snap, err := noop.TakeSnapshot(context.Background())
	if err != nil {
		t.Fatalf("NoopSnapshotManager.TakeSnapshot failed: %v", err)
	}
	if snap == nil || snap.ID == "" {
		t.Fatalf("unexpected nil or empty snapshot from NoopSnapshotManager")
	}

	if err := noop.RestoreSnapshot(context.Background(), snap); err != nil {
		t.Fatalf("NoopSnapshotManager.RestoreSnapshot failed: %v", err)
	}
}

func TestRouterSnapshotManagerPropagatesErrors(t *testing.T) {
	expectedErr := errors.New("command failed: iptables-save permission denied")
	manager := NewSnapshotManager(
		nil,
		func(ctx context.Context) (string, error) {
			return "", expectedErr
		},
		nil,
		nil,
	)

	_, err := manager.TakeSnapshot(context.Background())
	if err == nil || !strings.Contains(err.Error(), "failed to snapshot iptables") {
		t.Fatalf("expected iptables snapshot error propagation, got: %v", err)
	}
}

func TestRouterSnapshotManagerRestoreNilSnapshot(t *testing.T) {
	manager := NewSnapshotManager(nil, nil, nil, nil)
	if err := manager.RestoreSnapshot(context.Background(), nil); err == nil {
		t.Fatal("expected error on nil snapshot restoration")
	}
}
