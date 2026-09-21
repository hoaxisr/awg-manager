package hydraroute

import "testing"

// stopRestartTimerOnCleanup гасит отложенный neo restart в конце теста.
// Любой write-путь (rules-write, geo-sync, config-heal…) при Installed=true
// планирует scheduleRestart → AfterFunc(2s); без гашения callback стреляет
// уже в чужом тесте и читает hrneoBinary/neoCommand без синхронизации
// (F410, race-job CI 21.09.2026). Регистрировать сразу после
// SetStatusForTest(true): Cleanup идёт LIFO, таймер гаснет раньше отката
// глобальных путей, которые тест мог подменить.
func stopRestartTimerOnCleanup(t *testing.T, s *Service) {
	t.Helper()
	t.Cleanup(func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.restartTimer != nil {
			s.restartTimer.Stop()
			s.restartTimer = nil
		}
	})
}
