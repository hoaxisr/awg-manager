package events

import (
	"bytes"
	"net/url"
	"os"
	"strings"
)

// DefaultSpoolPath — файл, в который хук-скрипт дописывает по строке на
// событие NDMS. tmpfs: повторяющаяся запись не должна идти на флеш.
// Свой каталог: в /var/run/awg-manager пишет и журнал sing-box, а watch
// стоит на каталог — чужие записи будили бы читателя. Каталог же служит
// хук-скрипту признаком «демон читает»: Start его создаёт, Stop сносит.
const DefaultSpoolPath = "/var/run/awg-manager/hooks/ndm-hooks"

// spoolCap — размер, после которого файл ротируется в path+".1".
const spoolCap = 256 << 10

// maxPendingLine — потолок недописанной строки (как в singbox/proclog.go).
// Строка хука ≈100 байт; хвост длиннее — мусор (порванная запись): он
// выбрасывается с одним Warn, чтение продолжается со следующего '\n'.
const maxPendingLine = 64 << 10

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
	// rotateWarned — отказ ротации уже в журнале. Ротация повторяется на
	// каждом drain, пока файл больше cap; без флага каждый хук давал бы Warn.
	// Сбрасывается успешной ротацией.
	rotateWarned bool
	done         chan struct{}
}

type spoolFile struct {
	f    *os.File
	pos  int64
	buf  []byte // хвост без '\n' — дочитается на следующем событии
	skip bool   // хвост превысил maxPendingLine — пропускать до '\n'
}

// NewSpoolReader создаёт читателя; читать начинает Start.
func NewSpoolReader(path string, sink func(Event), log Logger) *SpoolReader {
	if log == nil {
		log = NopLogger()
	}
	return &SpoolReader{path: path, sink: sink, log: log, cap: spoolCap}
}

// drain дочитывает сначала прежний файл, потом текущий, и ротирует текущий,
// если он дорос до cap. Недописанная строка ротации не мешает: её хвост
// допишет тот же процесс в тот же inode (теперь .1), а у prev свой буфер.
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
	if r.cur.pos >= r.cap {
		r.rotate()
	}
}

// rotate переименовывает текущий файл в path+".1" и начинает новый.
// ftruncate здесь нельзя: запись, пришедшая между проверкой размера и
// усечением, пропала бы. После rename писатель, открывший файл раньше,
// пишет в .1 — его fd держим и дочитываем до следующей ротации.
func (r *SpoolReader) rotate() {
	if err := os.Rename(r.path, r.path+".1"); err != nil {
		// Читаем текущий файл дальше: он лишь растёт сверх cap.
		if !r.rotateWarned {
			r.log.Warnf("spool: rotate %s: %v", r.path, err)
			r.rotateWarned = true
		}
		return
	}
	r.rotateWarned = false
	if r.prev != nil {
		if len(r.prev.buf) > 0 {
			r.log.Warnf("spool: dropped partial line (%d bytes) of %s.1", len(r.prev.buf), r.path)
		}
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
		if sf.skip {
			if i < 0 {
				sf.buf = sf.buf[:0]
				return
			}
			sf.buf = append(sf.buf[:0], sf.buf[i+1:]...)
			sf.skip = false
			continue
		}
		if i < 0 {
			if len(sf.buf) > maxPendingLine {
				r.log.Warnf("spool: dropped partial line over %d bytes", maxPendingLine)
				sf.buf = sf.buf[:0]
				sf.skip = true
			}
			return
		}
		if i > maxPendingLine {
			// Хвост дописался в том же чтении, что и перебор: та же порванная
			// запись, что и в ветке выше.
			r.log.Warnf("spool: dropped partial line over %d bytes", maxPendingLine)
			sf.buf = append(sf.buf[:0], sf.buf[i+1:]...)
			continue
		}
		line := string(sf.buf[:i])
		sf.buf = append(sf.buf[:0], sf.buf[i+1:]...)
		if line == "" {
			continue
		}
		ev, err := ParseHookForm(spoolValues(line))
		if err != nil {
			r.log.Warnf("spool: bad line %q: %v", line, err)
			continue
		}
		r.sink(ev)
	}
}

// spoolValues разбирает строку spool: пары через '&', ключ до первого '='.
// Значения сырые, без percent-декодирования: скрипт пишет их как есть, и
// url.ParseQuery на `%` в значении (IPv6 с зоной, fe80::1%br0) отверг бы
// строку целиком — событие пропало бы.
func spoolValues(line string) url.Values {
	v := url.Values{}
	for _, kv := range strings.Split(line, "&") {
		k, val, _ := strings.Cut(kv, "=")
		v.Add(k, val)
	}
	return v
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
