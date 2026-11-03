//go:build linux

package procport

import (
	"fmt"
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

// killTarget — процесс, прошедший проверку, и конфликт, ради которого его
// останавливают. Номер сам по себе идентичностью не является: между обходом
// /proc и сигналом процесс может завершиться, а номер достаться другому.
// Поэтому сигнал получает только тот же (pid, starttime), пока он держит
// конфликтующий сокет.
type killTarget struct {
	pid   int
	start uint64
	proto Proto
	want  addrMatch
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
// процессу, получившему тот же номер. Ошибка — проверить не удалось.
func (t killTarget) holdsListener() (bool, error) {
	if !t.sameProcess() {
		return false, nil
	}
	inodes, err := listenerInodes(t.proto, t.want)
	if err != nil {
		return false, err
	}
	if len(inodes) == 0 {
		return false, nil
	}
	owns, err := pidOwnsInodes(t.pid, inodes)
	if err != nil {
		return false, err
	}
	return owns && t.sameProcess(), nil
}

// terminatePID — SIGTERM, затем SIGKILL по истечении grace. Добивается только
// тот же процесс и только пока он держит конфликтующий сокет: процесс, который
// по SIGTERM закрыл сокет и дочищает работу, порт уже освободил, а номер за
// grace мог достаться другому. Не удалось проверить — SIGKILL не уходит.
func terminatePID(t killTarget) error {
	if err := sigTerm(t.pid); err != nil {
		return err
	}
	deadline := time.Now().Add(killGrace)
	for {
		held, err := t.holdsListener()
		if err != nil {
			return fmt.Errorf("процесс %d после SIGTERM не удалось проверить — SIGKILL не отправлен: %w", t.pid, err)
		}
		if !held {
			return nil
		}
		if !time.Now().Before(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	return sigKill(t.pid)
}
