package api

import (
	"bytes"
	"io"
	"os"
	"sync"

	"github.com/hoaxisr/awg-manager/internal/logging"
)

// autolistLogDefaultPath — журнал сателлита autolist (RAMLog в /tmp на роутере,
// см. awgmautolist/internal/logging). Панель его не пишет — только читает.
const autolistLogDefaultPath = "/tmp/awgmautolist.log"

const (
	// autolistLogImportBytes/autolistLogImportLines — потолки ПЕРВОГО импорта:
	// в общий журнал уходит хвост файла, а не все 5 MiB RAMLog'а сателлита.
	autolistLogImportBytes = 64 << 10
	autolistLogImportLines = 200
)

// autolistLogSync — pull-based tail журнала сателлита в app-журнал панели.
// Sync вызывается из Status (его опрос и так периодический), состояние
// (offset + незавершённый хвост) живёт между вызовами под мьютексом.
// Ошибки файла проглатываются: нет файла — нет событий, /status не падает.
type autolistLogSync struct {
	// path — seam только для тестов; пусто — autolistLogDefaultPath.
	path string

	mu      sync.Mutex
	started bool   // первичный импорт выполнен
	offset  int64  // файл прочитан до этого байта
	pending []byte // незавершённая строка (хвост без '\n')
}

// sync доталкивает файл до текущего конца, отправляя каждую новую ПОЛНУЮ
// строку через log.Info("autolist", "", line). action="autolist" — идентификатор
// источника в общем журнале; дальше события фильтрует и публикует панель.
func (s *autolistLogSync) sync(log *logging.ScopedLogger) {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.path
	if path == "" {
		path = autolistLogDefaultPath
	}
	f, err := os.Open(path)
	if err != nil {
		return // файла нет (сателлит не ставился/ещё не писал) — просто нет событий
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return
	}
	size := fi.Size()

	first := !s.started
	switch {
	case first:
		s.started = true
		s.offset = size - autolistLogImportBytes
		if s.offset < 0 {
			s.offset = 0
		}
	case size < s.offset:
		// RAMLog усекает файл при 5 MiB: всё, что сейчас в файле, новое —
		// читаем актуальный файл с нуля, дублей не будет.
		s.offset = 0
		s.pending = s.pending[:0]
	}
	if size == s.offset {
		return // новых байт нет; незавершённый хвост ждёт в pending
	}

	chunk := make([]byte, size-s.offset)
	n, err := f.ReadAt(chunk, s.offset)
	if err != nil && err != io.EOF {
		return // прочитать не смогли — позицию не двигаем
	}
	chunk = chunk[:n]
	if first && s.offset > 0 {
		// врезались в середину строки — неполную голову выкидываем
		if i := bytes.IndexByte(chunk, '\n'); i >= 0 {
			chunk = chunk[i+1:]
		} else {
			chunk = nil
		}
	}

	merged := make([]byte, 0, len(s.pending)+len(chunk))
	merged = append(merged, s.pending...)
	merged = append(merged, chunk...)
	var lines []string
	for {
		i := bytes.IndexByte(merged, '\n')
		if i < 0 {
			break
		}
		line := string(merged[:i])
		merged = merged[i+1:]
		if first {
			lines = append(lines, line) // потолок строк применим ниже, к хвосту
		} else if log != nil {
			log.Info("autolist", "", line)
		}
	}
	if first {
		if len(lines) > autolistLogImportLines {
			lines = lines[len(lines)-autolistLogImportLines:]
		}
		for _, line := range lines {
			if log != nil {
				log.Info("autolist", "", line)
			}
		}
	}
	s.pending = append(s.pending[:0], merged...)
	s.offset += int64(n)
}
