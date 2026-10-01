package aiassistant

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type SystemSnapshot struct {
	ID        string            `json:"id"`
	CreatedAt time.Time         `json:"createdAt"`
	IPTables  string            `json:"iptables,omitempty"`
	Services  map[string]string `json:"services,omitempty"`
	Routing   string            `json:"routing,omitempty"`
}

type SnapshotManager interface {
	TakeSnapshot(ctx context.Context) (*SystemSnapshot, error)
	RestoreSnapshot(ctx context.Context, snapshot *SystemSnapshot) error
}

// NoopSnapshotManager provides a no-op snapshot manager for tests or environments without system mutation permissions.
type NoopSnapshotManager struct{}

func NewNoopSnapshotManager() SnapshotManager {
	return &NoopSnapshotManager{}
}

func (n *NoopSnapshotManager) TakeSnapshot(_ context.Context) (*SystemSnapshot, error) {
	return &SystemSnapshot{
		ID:        fmt.Sprintf("snap_noop_%d", time.Now().UnixMilli()),
		CreatedAt: time.Now(),
		Services:  make(map[string]string),
	}, nil
}

func (n *NoopSnapshotManager) RestoreSnapshot(_ context.Context, _ *SystemSnapshot) error {
	return nil
}

// RouterSnapshotManager captures and restores system state on router platforms.
type RouterSnapshotManager struct {
	execCommand func(ctx context.Context, cmd string) (string, error)
	iptablesCmd func(ctx context.Context) (string, error)
	routingCmd  func(ctx context.Context) (string, error)
	servicesCmd func(ctx context.Context) (map[string]string, error)
}

func NewSnapshotManager(
	execCmd func(context.Context, string) (string, error),
	iptCmd func(context.Context) (string, error),
	rtCmd func(context.Context) (string, error),
	srvCmd func(context.Context) (map[string]string, error),
) SnapshotManager {
	return &RouterSnapshotManager{
		execCommand: execCmd,
		iptablesCmd: iptCmd,
		routingCmd:  rtCmd,
		servicesCmd: srvCmd,
	}
}

func (s *RouterSnapshotManager) TakeSnapshot(ctx context.Context) (*SystemSnapshot, error) {
	now := time.Now()
	id := fmt.Sprintf("snap_%d", now.UnixMilli())

	snap := &SystemSnapshot{
		ID:        id,
		CreatedAt: now,
		Services:  make(map[string]string),
	}

	// Dump iptables
	if s.iptablesCmd != nil {
		ipt, err := s.iptablesCmd(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to snapshot iptables: %w", err)
		}
		snap.IPTables = ipt
	} else if s.execCommand != nil {
		out, err := s.execCommand(ctx, "iptables-save")
		if err == nil {
			snap.IPTables = out
		}
	}

	// Dump routing rules
	if s.routingCmd != nil {
		rt, err := s.routingCmd(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to snapshot routing: %w", err)
		}
		snap.Routing = rt
	} else if s.execCommand != nil {
		out, err := s.execCommand(ctx, "ip rule show")
		if err == nil {
			snap.Routing = out
		}
	}

	// Dump services state
	if s.servicesCmd != nil {
		srvs, err := s.servicesCmd(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to snapshot services: %w", err)
		}
		snap.Services = srvs
	}

	return snap, nil
}

func (s *RouterSnapshotManager) RestoreSnapshot(ctx context.Context, snap *SystemSnapshot) error {
	if snap == nil {
		return fmt.Errorf("cannot restore nil snapshot")
	}
	if s.execCommand == nil {
		return fmt.Errorf("restore requires command execution capability")
	}

	var errorsOccurred []string

	// If iptables dump exists, restore it via pipe to iptables-restore
	if snap.IPTables != "" {
		// Escape or pass safely to iptables-restore
		restoreCmd := fmt.Sprintf("printf '%%s\\n' %q | iptables-restore", snap.IPTables)
		if _, err := s.execCommand(ctx, restoreCmd); err != nil {
			errorsOccurred = append(errorsOccurred, fmt.Sprintf("restore iptables failed: %v", err))
		}
	}

	// If services were captured, attempt to restore running state if needed
	if len(snap.Services) > 0 {
		for srv, state := range snap.Services {
			action := "stop"
			if state == "running" || state == "true" {
				action = "start"
			}
			cmd := fmt.Sprintf("/opt/etc/init.d/%s %s", srv, action)
			if _, err := s.execCommand(ctx, cmd); err != nil {
				errorsOccurred = append(errorsOccurred, fmt.Sprintf("restore service %s failed: %v", srv, err))
			}
		}
	}

	if len(errorsOccurred) > 0 {
		return fmt.Errorf("snapshot rollback encountered errors: %s", strings.Join(errorsOccurred, "; "))
	}
	return nil
}
