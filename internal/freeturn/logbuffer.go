package freeturn

import (
	"strings"
	"sync"
)

// processLogMaxLines — сколько последних строк stdout/stderr держим для UI.
// При -n=10 и captcha-лавине 80 строк исчезали за секунды.
const processLogMaxLines = 500

// ringBuffer keeps the last `max` lines written to it. Used to retain a
// short stderr tail so a startup failure (bad flags, missing -peer,
// captcha required, etc) can surface a useful message instead of just
// "exit status 1".
type ringBuffer struct {
	mu    sync.Mutex
	lines []string
	max   int
}

func newRingBuffer(max int) *ringBuffer {
	return &ringBuffer{max: max}
}

func (b *ringBuffer) WriteLine(line string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, line)
	if len(b.lines) > b.max {
		b.lines = b.lines[len(b.lines)-b.max:]
	}
}

func (b *ringBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Join(b.lines, "\n")
}

// Reset clears the buffer in place. Used instead of allocating a fresh
// ringBuffer on each process restart, so a concurrent reader (Status(), via
// the API) never observes a half-swapped pointer.
func (b *ringBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = nil
}

// LastLines returns at most the last n lines, joined by "\n". Used for a
// short crash excerpt (LastError) as opposed to String()'s full tail (Log) —
// a freeturn client can run for hours and accumulate many benign
// connect/disconnect lines, so dumping the whole buffer as "the error" is
// noise; the last few lines are far more likely to contain the actual
// fatal message.
func (b *ringBuffer) LastLines(n int) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n <= 0 || len(b.lines) == 0 {
		return ""
	}
	if n > len(b.lines) {
		n = len(b.lines)
	}
	return strings.Join(b.lines[len(b.lines)-n:], "\n")
}
