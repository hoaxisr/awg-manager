package singbox

import "sync"

type limitedBuffer struct {
	mu  sync.Mutex
	max int
	buf []byte
}

func newLimitedBuffer(max int) *limitedBuffer {
	return &limitedBuffer{max: max, buf: make([]byte, 0, 256)}
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	free := b.max - len(b.buf)
	if free <= 0 {
		return len(p), nil
	}
	n := len(p)
	if n > free {
		n = free
	}
	b.buf = append(b.buf, p[:n]...)
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}
