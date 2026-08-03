package server

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/hoaxisr/awg-manager/internal/backup"
	"github.com/hoaxisr/awg-manager/internal/orchestrator"
)

const backupQuiesceTimeout = 2 * time.Minute

// QuiesceForBackup stops awg-manager child processes (FreeTurn, WDTT, sing-box,
// WG tunnels) without disabling them in config. The awg-manager daemon itself
// keeps running so the HTTP handler can finish the export/import response.
func (s *Server) QuiesceForBackup(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, backupQuiesceTimeout)
	defer cancel()

	runDir := filepath.Join(s.settings.DataDir(), "run")

	if s.pingCheckService != nil {
		s.pingCheckService.StopMonitoringAll()
	}

	if s.freeturnService != nil {
		s.freeturnService.Stop()
	}
	if s.wdttService != nil {
		s.wdttService.Stop()
	}
	backup.KillOrphanProxyProcesses(runDir)

	if s.singboxOp != nil {
		if err := s.singboxOp.QuiesceStop(ctx); err != nil {
			return fmt.Errorf("sing-box: %w", err)
		}
	}

	if s.ndmsSaveCoord != nil {
		if err := s.ndmsSaveCoord.Flush(ctx); err != nil {
			s.appLog.Warn("backup-quiesce", "", "ndms flush: "+err.Error())
		}
	}

	if s.orch != nil {
		s.orch.LoadState(ctx)
		if err := s.orch.HandleEvent(ctx, orchestrator.Event{Type: orchestrator.EventQuiesce}); err != nil {
			return fmt.Errorf("tunnels: %w", err)
		}
	}

	// Second pass: quiesce can leave stale pid files if a process exited between
	// registry stop and our walk.
	backup.KillOrphanProxyProcesses(runDir)

	return nil
}

// ResumeAfterBackup brings child services back after a successful export quiesce.
// Restore path relies on daemon restart instead (ScheduleRestart).
func (s *Server) ResumeAfterBackup(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, backupQuiesceTimeout)
	defer cancel()

	if s.orch != nil {
		s.orch.LoadState(ctx)
		_ = s.orch.HandleEvent(ctx, orchestrator.Event{Type: orchestrator.EventReconnect})
	}

	go func() {
		if s.freeturnService != nil {
			type resumer interface{ ResumeEnabled() }
			if r, ok := s.freeturnService.(resumer); ok {
				r.ResumeEnabled()
			}
		}
		if s.wdttService != nil {
			type resumer interface{ ResumeEnabled() }
			if r, ok := s.wdttService.(resumer); ok {
				r.ResumeEnabled()
			}
		}
	}()
}
