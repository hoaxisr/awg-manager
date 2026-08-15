//go:build linux

package awgmproto

import (
	"context"
	"os"
	"os/signal"
	"time"

	"golang.org/x/sys/unix"
)

// RedirectStdio направляет stdout и stderr процесса в журнал.
//
// Форки печатают событиями через fmt.Printf и log — переписывать каждое место
// печати незачем: dup2 накрывает их все разом. Заодно исчезает SIGPIPE: файл, в
// отличие от пайпа к умершему менеджеру, всегда доступен на запись.
//
// Хелперов это НЕ накрывает автоматически, вопреки соблазну так думать:
// os/exec подставляет дочернему процессу /dev/null, когда cmd.Stdout и
// cmd.Stderr не заданы, — унаследованные 1 и 2 достаются только тому, кому их
// назначили явно. Хелперы, чей вывод читают предикаты здоровья, обязаны
// получить cmd.Stdout = os.Stdout (или Log.Fd()) в самой обвязке.
func RedirectStdio(l *Log) error {
	fd := int(l.Fd())
	if err := unix.Dup3(fd, 1, 0); err != nil {
		return err
	}
	return unix.Dup3(fd, 2, 0)
}

// IgnoreSIGPIPE делает запись в закрытый stdout ошибкой EPIPE вместо смерти
// процесса.
//
// Без этого Go убивает процесс сигналом при записи в закрытый дескриптор 1 или
// 2 — ровно то, что происходит с усыновляемым процессом, когда менеджер умирает
// и read-конец пайпа закрывается. Нужно и при ручном запуске, где журнала нет.
func IgnoreSIGPIPE() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, unix.SIGPIPE)
	go func() {
		for range ch {
		}
	}()
}

// EnforceCap усекает журнал, если он перерос потолок.
//
// Размер спрашивается у ядра, а не берётся из счётчика Write: после
// RedirectStdio форк пишет прямо в дескриптор 1, мимо Log.Write, и счётчик
// такой записи не видит. O_APPEND держит записи в конец файла, поэтому усечение
// не оставляет дыры из нулевых байтов даже при живых параллельных писателях.
func (l *Log) EnforceCap() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, err := l.f.Stat()
	if err != nil {
		return err
	}
	if st.Size() <= LogCapBytes {
		return nil
	}
	if err := l.f.Truncate(0); err != nil {
		return err
	}
	l.size = 0
	return nil
}

// WatchCap следит за потолком, пока жив контекст. Ошибки записи и усечения
// глотаются намеренно: журнал не является условием работоспособности туннеля.
func (l *Log) WatchCap(ctx context.Context, period time.Duration) {
	t := time.NewTicker(period)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = l.EnforceCap()
		}
	}
}
