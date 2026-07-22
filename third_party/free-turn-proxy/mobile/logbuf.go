package mobile

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

const logBufMax = 500

type logBuffer struct {
	mu    sync.Mutex
	lines []string
}

func (b *logBuffer) append(line string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, line)
	if len(b.lines) > logBufMax {
		b.lines = b.lines[len(b.lines)-logBufMax:]
	}
}

func (b *logBuffer) get() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Join(b.lines, "\n")
}

func (b *logBuffer) clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = b.lines[:0]
}

var sharedLogBuf = &logBuffer{}

// GetLogs возвращает последние логи фреймворка (до 500 строк).
func GetLogs() string { return sharedLogBuf.get() }

// ClearLogs очищает буфер логов.
func ClearLogs() { sharedLogBuf.clear() }

// bufLogger реализует logx.Logger, пишет в stderr и в sharedLogBuf.
type bufLogger struct{ debug bool }

func (*bufLogger) write(level, format string, v ...any) {
	msg := fmt.Sprintf(format, v...)
	line := time.Now().Format("15:04:05") + " [" + level + "] " + msg
	log.Print("[" + level + "] " + msg)
	sharedLogBuf.append(line)
}

func (l *bufLogger) Debugf(format string, v ...any) {
	if l.debug {
		l.write("DBG", format, v...)
	}
}
func (l *bufLogger) Infof(format string, v ...any)  { l.write("INF", format, v...) }
func (l *bufLogger) Warnf(format string, v ...any)  { l.write("WRN", format, v...) }
func (l *bufLogger) Errorf(format string, v ...any) { l.write("ERR", format, v...) }
func (l *bufLogger) DebugEnabled() bool             { return l.debug }
