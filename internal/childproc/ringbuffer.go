package childproc

import (
	"strings"
	"sync"
)

// RingBuffer keeps the last `max` lines written to it. Used to retain a
// short stderr tail so a startup failure (bad flags, missing peer, captcha
// required, etc) can surface a useful message instead of just
// "exit status 1".
type RingBuffer struct {
	mu    sync.Mutex
	lines []string
	max   int
}

func NewRingBuffer(max int) *RingBuffer {
	return &RingBuffer{max: max}
}

func (b *RingBuffer) WriteLine(line string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, line)
	if len(b.lines) > b.max {
		b.lines = b.lines[len(b.lines)-b.max:]
	}
}

func (b *RingBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Join(b.lines, "\n")
}

// Reset clears the buffer in place. Used instead of allocating a fresh
// RingBuffer on each process restart, so a concurrent reader (Status(), via
// the API) never observes a half-swapped pointer.
func (b *RingBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = nil
}

// LastLines returns at most the last n lines, joined by "\n". Used for a
// short crash excerpt (LastError) as opposed to String()'s full tail (Log) —
// a daemon can run for hours and accumulate many benign connect/disconnect
// lines, so dumping the whole buffer as "the error" is noise; the last few
// lines are far more likely to contain the actual fatal message.
func (b *RingBuffer) LastLines(n int) string {
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
