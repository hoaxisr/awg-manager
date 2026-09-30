package events

import (
	"bytes"
	"net/url"
	"os"
)

// DefaultSpoolPath — файл, в который хук-скрипт дописывает по строке на
// событие NDMS. tmpfs: повторяющаяся запись не должна идти на флеш.
const DefaultSpoolPath = "/var/run/awg-manager/ndm-hooks"

// spoolCap — размер, после которого файл ротируется в path+".1".
const spoolCap = 256 << 10

// SpoolReader читает строки хуков из append-only файла на tmpfs в порядке
// записи и отдаёт их sink по одной. Один читатель на процесс.
//
// NDMS исполняет хуки очередью, поэтому порядок строк в файле = порядок
// событий; sink вызывается из одной горутины в том же порядке.
type SpoolReader struct {
	path string
	sink func(Event)
	log  Logger
	cap  int64

	cur  *spoolFile // текущий path
	prev *spoolFile // path+".1" после ротации: живёт до следующей
	ino  *os.File   // inotify (только linux)
	done chan struct{}
}

type spoolFile struct {
	f   *os.File
	pos int64
	buf []byte // хвост без '\n' — дочитается на следующем событии
}

// NewSpoolReader создаёт читателя; читать начинает Start.
func NewSpoolReader(path string, sink func(Event), log Logger) *SpoolReader {
	if log == nil {
		log = NopLogger()
	}
	return &SpoolReader{path: path, sink: sink, log: log, cap: spoolCap}
}

// drain дочитывает сначала прежний файл, потом текущий, и ротирует текущий,
// если он дорос до cap и не оборван на полустроке.
func (r *SpoolReader) drain() {
	if r.prev != nil {
		r.drainFile(r.prev)
	}
	if r.cur == nil {
		f, err := os.OpenFile(r.path, os.O_RDONLY|os.O_CREATE, 0o644)
		if err != nil {
			r.log.Warnf("spool: open %s: %v", r.path, err)
			return
		}
		r.cur = &spoolFile{f: f}
	}
	r.drainFile(r.cur)
	if r.cur.pos >= r.cap && len(r.cur.buf) == 0 {
		r.rotate()
	}
}

// rotate переименовывает текущий файл в path+".1" и начинает новый.
// ftruncate здесь нельзя: запись, пришедшая между проверкой размера и
// усечением, пропала бы. После rename писатель, открывший файл раньше,
// пишет в .1 — его fd держим и дочитываем до следующей ротации.
func (r *SpoolReader) rotate() {
	if err := os.Rename(r.path, r.path+".1"); err != nil {
		r.log.Warnf("spool: rotate %s: %v", r.path, err)
		return
	}
	if r.prev != nil {
		r.prev.f.Close()
	}
	r.prev, r.cur = r.cur, nil
	f, err := os.OpenFile(r.path, os.O_RDONLY|os.O_CREATE, 0o644)
	if err != nil {
		// Следующий drain попробует снова; писатель создаст файл сам (>>).
		r.log.Warnf("spool: open %s: %v", r.path, err)
		return
	}
	r.cur = &spoolFile{f: f}
}

func (r *SpoolReader) drainFile(sf *spoolFile) {
	var chunk [4096]byte
	for {
		n, err := sf.f.Read(chunk[:])
		if n > 0 {
			sf.pos += int64(n)
			sf.buf = append(sf.buf, chunk[:n]...)
			r.emitLines(sf)
		}
		if err != nil || n == 0 {
			return
		}
	}
}

func (r *SpoolReader) emitLines(sf *spoolFile) {
	for {
		i := bytes.IndexByte(sf.buf, '\n')
		if i < 0 {
			return
		}
		line := string(sf.buf[:i])
		sf.buf = append(sf.buf[:0], sf.buf[i+1:]...)
		if line == "" {
			continue
		}
		v, err := url.ParseQuery(line)
		if err != nil {
			r.log.Warnf("spool: bad line %q: %v", line, err)
			continue
		}
		ev, err := ParseHookForm(v)
		if err != nil {
			r.log.Warnf("spool: bad line %q: %v", line, err)
			continue
		}
		r.sink(ev)
	}
}

// closeFiles закрывает файлы после остановки горутины чтения.
func (r *SpoolReader) closeFiles() {
	for _, sf := range []*spoolFile{r.cur, r.prev} {
		if sf != nil {
			sf.f.Close()
		}
	}
	r.cur, r.prev = nil, nil
}
