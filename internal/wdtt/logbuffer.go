package wdtt

import (
	"strings"
	"sync"
)

const processLogMaxLines = 500

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

func (b *ringBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = nil
}

func (b *ringBuffer) LastLines(n int) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n <= 0 || len(b.lines) == 0 {
		return ""
	}
	start := 0
	if len(b.lines) > n {
		start = len(b.lines) - n
	}
	return strings.Join(b.lines[start:], "\n")
}
