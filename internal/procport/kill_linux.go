//go:build linux

package procport

import (
	"time"

	"github.com/hoaxisr/awg-manager/internal/childproc"
)

// Швы для тестов: подменяются, чтобы ни один тест не послал сигнал настоящему
// процессу машины, на которой он запущен.
var (
	sigTerm          = childproc.Terminate
	sigKill          = childproc.Kill
	processStartTime = childproc.StartTime
	killGrace        = 3 * time.Second
)

// killTarget — процесс, прошедший проверку. Номер сам по себе идентичностью не
// является: между обходом /proc и сигналом процесс может завершиться, а номер
// достаться другому. Поэтому сигнал получает только тот же (pid, starttime).
type killTarget struct {
	pid   int
	start uint64
}

// sameProcess — под номером всё ещё тот процесс, что прошёл проверку.
// Нечитаемый /proc/<pid>/stat — процесса нет, сигналить некого.
func (t killTarget) sameProcess() bool {
	st, ok := processStartTime(t.pid)
	return ok && st == t.start
}

// holdsListener — процесс всё ещё держит сокет, конфликтующий с want.
// Таблица /proc/net перечитывается заново: сокет мог закрыться. Отпечаток
// сверяется до и после чтения fd — иначе fd могли принадлежать новому
// процессу, получившему тот же номер.
func (t killTarget) holdsListener(proto Proto, want addrMatch) bool {
	if !t.sameProcess() {
		return false
	}
	inodes := listenerInodes(proto, want)
	if len(inodes) == 0 || !pidOwnsInodes(t.pid, inodes) {
		return false
	}
	return t.sameProcess()
}

// terminatePID — SIGTERM, затем SIGKILL по истечении grace. Добивается только
// тот же процесс: за grace номер мог освободиться и достаться другому.
func terminatePID(t killTarget) error {
	if err := sigTerm(t.pid); err != nil {
		return err
	}
	deadline := time.Now().Add(killGrace)
	for time.Now().Before(deadline) {
		if !t.sameProcess() {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !t.sameProcess() {
		return nil
	}
	return sigKill(t.pid)
}
