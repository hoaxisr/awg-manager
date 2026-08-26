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

// QuiesceForBackup stops awg-manager child processes (proxy instances,
// sing-box, WG tunnels) without disabling them in config. The awg-manager daemon itself
// keeps running so the HTTP handler can finish the export/import response.
func (s *Server) QuiesceForBackup(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, backupQuiesceTimeout)
	defer cancel()

	runDir := filepath.Join(s.settings.DataDir(), "run")

	if s.pingCheckService != nil {
		s.pingCheckService.StopMonitoringAll()
	}

	// Воркеры снимаются ДО добивания процессов: живой воркер поднял бы
	// убитый инстанс обратно прямо посреди экспорта.
	if s.proxyRuntime != nil {
		s.proxyRuntime.Shutdown()
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

	// Горутиной: Boot ходит в RCI и держал бы ответ на экспорт. Инстансы
	// пересобираются с нуля — Shutdown очистил карту менеджера.
	go func() {
		if s.proxyRuntime == nil {
			return
		}
		if err := s.proxyRuntime.Boot(context.Background()); err != nil {
			s.appLog.Warn("backup-resume", "", "прокси-рантайм не поднялся: "+err.Error())
		}
	}()
}
