// internal/singbox/proclog.go
package singbox

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

// Файловые логи процесса sing-box (вместо пайпов): пайпы к демону убивали
// осиротевший sing-box SIGPIPE'ом при первой записи после смерти
// awg-manager (Go завершает процесс на EPIPE в fd 1/2) — что делало
// невозможной адопцию живого процесса при рестарте демона. Файлы лежат
// на tmpfs (/var -> /tmp, проверено на роутере): flash не изнашивается,
// после ребута логи не нужны (процесс мёртв). Var-seam'ы — для тестов.
var (
	procLogDir     = "/var/run/awg-manager"
	procOutLogName = "sing-box.out.log"
	procErrLogName = "sing-box.err.log"
)

const procLogTailPoll = 500 * time.Millisecond

// maxPendingLine caps the partial-line buffer in tailFile. A pathological
// newline-free stream must not grow it unbounded (the old bufio.Scanner
// this replaced capped tokens at 64KB); on overflow the accumulated bytes
// are flushed as one line instead of losing them.
const maxPendingLine = 64 * 1024

// procLogMaxBytes — потолок размера log-файла живого процесса. tail сам
// усекает файл при переполнении (self-ротация): писатель держит fd с
// O_APPEND, после truncate ядро продолжит запись с offset 0. Без этого
// долгоживущий adopted/спавненный sing-box на debug-уровне за месяцы
// аптайма съел бы tmpfs (124M на роутере). Var — seam для тестов; atomic,
// потому что tail-горутина живёт ещё 2*procLogTailPoll после Stop() и
// читает потолок, пока соседний тест его переставляет (F127).
var procLogMaxBytes atomic.Int64

func init() { procLogMaxBytes.Store(4 * 1024 * 1024) }

// openProcLog открывает файл лога; truncate=true — новый спавн начинает
// лог своего поколения с нуля.
func openProcLog(path string, truncate bool) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	// O_APPEND обязателен и в truncate-ветке: без него у писателя (child
	// sing-box) позиционный fd, и после self-ротации (Truncate ниже в
	// tailFile) каждая его запись уходит на старое смещение — файл
	// становится sparse, tail вычитывает дыру из NUL-байтов и раздувает
	// ОЗУ демона их обработкой (#562).
	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if truncate {
		flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC | os.O_APPEND
	}
	return os.OpenFile(path, flags, 0644)
}

// tailFile поллит файл с offset'а и отдаёт ЦЕЛЫЕ строки в onLine.
// fromEnd=true стартует с конца (адопция: не реиграть старые строки в
// app-log), false — с нуля (свежий спавн). До первой позиции (offset<0)
// переживает отсутствие файла (ждёт появления). Завершается по ctx, а
// также САМ (return) при truncate или исчезновении файла ПОСЛЕ того как
// позиция уже выбрана — это конец жизни данного поколения: truncate/
// удаление файла под уже запозиционированным tail'ом означает, что его
// место занял (или скоро займёт) новый спавн, а не что этот же файл
// "уменьшился" сам по себе. Раньше truncate трактовался как "перечитать с
// нуля" — если новый Start усекал файл в узком окне между смертью старого
// поколения и cancel() его tail'а, старый tail реиграл строки НОВОГО
// поколения с байта 0 и они долетали до onLine дважды (I1).
//
// Инвариант: offset наращивается на КАЖДЫЙ прочитанный байт (включая
// незавершённый хвост без '\n' — он остаётся в pending и учитывается уже
// как потреблённый из файла), а onLine зовётся только когда накопленный
// pending заканчивается полной строкой. Это даёт корректное поведение и
// для строки, доехавшей до диска двумя кусками через границу poll'ов.
// pending также капается на maxPendingLine (I2) — pathological поток без
// '\n' не должен расти неограниченно.
func tailFile(ctx context.Context, path string, fromEnd bool, onLine func(string)) {
	var offset int64 = -1 // -1 = позиция ещё не выбрана
	var pending []byte
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(procLogTailPoll):
		}
		fi, err := os.Stat(path)
		if err != nil {
			if offset >= 0 {
				return // уже запозиционированы — файл исчез, поколение окончено
			}
			continue // позиция ещё не выбрана — ждём появления файла
		}
		if offset < 0 {
			if fromEnd {
				offset = fi.Size()
			} else {
				offset = 0
			}
		}
		if fi.Size() < offset { // truncate под нами — чужая (новая) генерация
			return
		}
		if fi.Size() == offset {
			// Self-ротация: файл полностью потреблён и превысил потолок —
			// усекаем САМИ и продолжаем то же поколение с offset 0 (писатель
			// держит fd с O_APPEND — ядро продолжит запись с нуля). Байты,
			// дописанные в TOCTOU-окно между Stat и Truncate, теряются —
			// окно микросекундное, для логов приемлемо. pending (частичная
			// строка, уже вычитанная из файла) доклеится из нового начала.
			// В отличие от чужого truncate выше — тот означает новый спавн.
			if offset > procLogMaxBytes.Load() {
				if err := os.Truncate(path, 0); err == nil {
					offset = 0
				}
			}
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			_ = f.Close()
			continue
		}
		r := bufio.NewReader(f)
		for {
			chunk, readErr := r.ReadBytes('\n')
			if len(chunk) > 0 {
				offset += int64(len(chunk)) // потреблённые байты файла, всегда
				// NUL-байты — sparse-дыра от писателя с позиционным fd
				// (sing-box, заспавненный сборкой до фикса #562 и живущий
				// через адопцию): после self-ротации его запись на старом
				// смещении оставляет дыру с начала файла. В логах NUL
				// легитимно не встречается — вырезаем, offset выше уже
				// учёл сырые байты.
				if bytes.IndexByte(chunk, 0) >= 0 {
					chunk = bytes.ReplaceAll(chunk, []byte{0}, nil)
				}
				pending = append(pending, chunk...)
			}
			if readErr != nil {
				// EOF — незавершённая строка остаётся в pending до
				// следующего poll'а, если не превысила cap; иначе
				// сбрасываем накопленное в onLine одной строкой, чтобы не
				// расти неограниченно (I2).
				if len(pending) > maxPendingLine {
					onLine(string(pending))
					pending = pending[:0]
				}
				break
			}
			line := string(pending[:len(pending)-1]) // без '\n'
			pending = pending[:0]
			onLine(line)
		}
		_ = f.Close()
	}
}

// readLogTail возвращает последние maxBytes файла ("" при ошибке/отсутствии).
// Используется для crash-диагностики (стартовая ошибка, OnExit-хвост)
// вместо прежнего pipe-буфера.
func readLogTail(path string, maxBytes int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return ""
	}
	off := fi.Size() - int64(maxBytes)
	if off < 0 {
		off = 0
	}
	b := make([]byte, fi.Size()-off)
	if _, err := f.ReadAt(b, off); err != nil && err != io.EOF {
		return ""
	}
	return string(b)
}
