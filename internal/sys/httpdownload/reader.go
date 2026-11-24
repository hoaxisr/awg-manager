// Package httpdownload provides shared utilities for streaming HTTP
// downloads with throttled progress reporting. Used by both the
// geo-data store and the singbox installer to surface
// live byte-stream progress over SSE without each caller maintaining
// its own throttled counter.
package httpdownload

import (
	"io"
	"time"
)

// emitDuration throttles periodic progress callbacks to avoid SSE spam.
const (
	emitDuration = 200 * time.Millisecond
)

// ProgressFn receives cumulative bytes downloaded and the expected total
// (0 if Content-Length was absent). Called from the goroutine driving
// the HTTP body — must not block.
type ProgressFn func(downloaded, total int64)

// Reader wraps an io.Reader and calls onProgress periodically with the
// cumulative bytes read so far. Throttled to emit at most every 200 ms,
// plus once on EOF so callers can
// render a final "100%" frame deterministically.
type Reader struct {
	r          io.Reader
	total      int64
	read       int64
	lastEmit   int64
	lastTime   time.Time
	onProgress ProgressFn
	now        func() time.Time
}

// NewReader wraps r with throttled progress reporting. total is the
// expected size (typically resp.ContentLength); pass 0 when unknown
// — callers that want a percent UI must handle total == 0.
func NewReader(r io.Reader, total int64, onProgress ProgressFn) *Reader {
	return newReaderWithClock(r, total, onProgress, time.Now)
}

func newReaderWithClock(r io.Reader, total int64, onProgress ProgressFn, now func() time.Time) *Reader {
	if now == nil {
		now = time.Now
	}
	return &Reader{
		r:          r,
		total:      total,
		lastTime:   now(),
		onProgress: onProgress,
		now:        now,
	}
}

// BytesRead returns total bytes consumed from the underlying reader.
func (p *Reader) BytesRead() int64 { return p.read }

func (p *Reader) Read(buf []byte) (int, error) {
	n, err := p.r.Read(buf)
	if n > 0 {
		p.read += int64(n)
		if p.onProgress != nil {
			now := p.now()
			// Emit periodic progress at most every emitDuration.
			shouldEmit := now.Sub(p.lastTime) >= emitDuration &&
				p.read > p.lastEmit
			if shouldEmit {
				p.onProgress(p.read, p.total)
				p.lastEmit = p.read
				p.lastTime = now
			}
		}
	}
	// Final flush on EOF guarantees a 100% frame even when the stream
	// signals EOF on a zero-length Read (typical for net/http bodies)
	// or when the last chunk was below the byte threshold.
	if err != nil && p.onProgress != nil && p.read != p.lastEmit {
		p.onProgress(p.read, p.total)
		p.lastEmit = p.read
	}
	return n, err
}
