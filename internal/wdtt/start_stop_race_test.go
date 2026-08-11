package wdtt

import (
	"path/filepath"
	"testing"
	"time"
)

// Стоп обязан дождаться идущего старта: оба пути пишут Enabled последним
// действием, и стоп, проскочивший вперёд, затирался обратно в true — инстанс
// оставался «включённым» вопреки решению пользователя, а супервизор поднимал
// его снова.
//
// Проверяем именно очередь на локе, а не весь путь остановки: id несуществующий,
// поэтому после захвата лока вызов сразу возвращает ошибку и не доходит до
// iptables/NDMS.
func assertStopWaitsForStart(t *testing.T, lockHeld func() (release func()), stop func() error) {
	t.Helper()
	release := lockHeld()
	done := make(chan error, 1)
	go func() { done <- stop() }()

	select {
	case <-done:
		release()
		t.Fatal("остановка не дождалась идущего старта")
	case <-time.After(100 * time.Millisecond):
	}

	release()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("остановка не завершилась после освобождения лока")
	}
}

func newRaceService(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	return NewService(dir, dir, filepath.Join(dir, "wdtt-client"), filepath.Join(dir, "wdtt-server"))
}

func TestStopServerInstance_WaitsForStartInFlight(t *testing.T) {
	s := newRaceService(t)
	assertStopWaitsForStart(t,
		func() func() {
			unlock, ok := s.tryLockServerStart("srv-1")
			if !ok {
				t.Fatal("лок старта сервера занят на старте теста")
			}
			return unlock
		},
		func() error { return s.StopServerInstance("srv-1") },
	)
}

func TestStopClientInstance_WaitsForStartInFlight(t *testing.T) {
	s := newRaceService(t)
	assertStopWaitsForStart(t,
		func() func() {
			unlock, ok := s.tryLockClientStart("cli-1")
			if !ok {
				t.Fatal("лок старта клиента занят на старте теста")
			}
			return unlock
		},
		func() error { return s.StopClientInstance("cli-1") },
	)
}

// Второй одновременный старт сервера отступает без RCI-работы — супервизор
// не должен лезть в NDMS поверх старта, идущего из API.
func TestStartServerInstance_SecondCallIsRejected(t *testing.T) {
	s := newRaceService(t)
	unlock, ok := s.tryLockServerStart("srv-1")
	if !ok {
		t.Fatal("лок старта сервера занят на старте теста")
	}
	defer unlock()

	if err := s.StartServerInstance("srv-1"); err != ErrServerStartInFlight {
		t.Fatalf("ожидали ErrServerStartInFlight, получили %v", err)
	}
}
