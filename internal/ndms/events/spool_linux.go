//go:build linux

package events

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Start открывает spool (создаёт при отсутствии), встаёт в его конец и
// подписывается через inotify на каталог. Накопленное до старта не читается:
// его покрывает бутовый список интерфейсов, поэтому Start обязан стоять до
// первого чтения списка и до установки хук-скриптов.
func (r *SpoolReader) Start() error {
	dir := filepath.Dir(r.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("spool: mkdir %s: %w", dir, err)
	}
	f, err := os.OpenFile(r.path, os.O_RDONLY|os.O_CREATE, 0o644)
	if err != nil {
		return fmt.Errorf("spool: open %s: %w", r.path, err)
	}
	pos, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		f.Close()
		return fmt.Errorf("spool: seek %s: %w", r.path, err)
	}
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		f.Close()
		return fmt.Errorf("spool: inotify: %w", err)
	}
	// Каталог, а не файл: после ротации запись в path+".1" и новый path
	// приходят событиями того же watch'а.
	if _, err := unix.InotifyAddWatch(fd, dir, unix.IN_MODIFY|unix.IN_CREATE); err != nil {
		unix.Close(fd)
		f.Close()
		return fmt.Errorf("spool: inotify watch %s: %w", dir, err)
	}
	r.cur = &spoolFile{f: f, pos: pos}
	// Неблокирующий fd встаёт в поллер рантайма: Close из Stop будит Read.
	r.ino = os.NewFile(uintptr(fd), "inotify")
	r.done = make(chan struct{})
	go r.loop()
	return nil
}

// Stop останавливает чтение, дожидается горутины и сносит каталог spool:
// хук-скрипт пишет, только пока каталог есть, так что остановленный демон
// не копит строки (следующий Start всё равно начал бы с конца файла).
func (r *SpoolReader) Stop() {
	if r.done == nil {
		return
	}
	r.ino.Close()
	<-r.done
	r.closeFiles()
	r.done = nil
	if err := os.RemoveAll(filepath.Dir(r.path)); err != nil {
		r.log.Warnf("spool: remove %s: %v", filepath.Dir(r.path), err)
	}
}

func (r *SpoolReader) loop() {
	defer close(r.done)
	// Строка, дописанная между Seek и AddWatch, события не дала.
	r.drain()
	names := [][]byte{[]byte(filepath.Base(r.path)), []byte(filepath.Base(r.path) + ".1")}
	var buf [4096]byte
	for {
		n, err := r.ino.Read(buf[:])
		if err != nil {
			if !errors.Is(err, os.ErrClosed) {
				r.log.Warnf("spool: inotify read: %v", err)
			}
			return
		}
		if spoolEventFor(buf[:n], names) {
			r.drain()
		}
	}
}

// spoolEventFor — есть ли в пачке событий inotify событие про наши файлы
// (в каталоге живут и чужие: stderr.log, obfuscator/) или переполнение
// очереди, после которого читать надо в любом случае.
func spoolEventFor(b []byte, names [][]byte) bool {
	for off := 0; off+unix.SizeofInotifyEvent <= len(b); {
		ev := (*unix.InotifyEvent)(unsafe.Pointer(&b[off]))
		end := off + unix.SizeofInotifyEvent + int(ev.Len)
		if end > len(b) {
			return true
		}
		if ev.Mask&unix.IN_Q_OVERFLOW != 0 {
			return true
		}
		name := bytes.TrimRight(b[off+unix.SizeofInotifyEvent:end], "\x00")
		for _, n := range names {
			if bytes.Equal(name, n) {
				return true
			}
		}
		off = end
	}
	return false
}
