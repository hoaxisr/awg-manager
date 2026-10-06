//go:build !linux

package procres

import (
	"sync"
	"time"
)

type otherLogWatcher struct {
	path     string
	offset   int64
	onFatal  func()
	stopCh   chan struct{}
	doneCh   chan struct{}
	stopOnce sync.Once
}

func newPlatformLogWatcher(path string, startOffset int64, onFatal func()) logWatcher {
	if startOffset < 0 {
		startOffset = 0
	}
	return &otherLogWatcher{
		path:    path,
		offset:  startOffset,
		onFatal: onFatal,
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
}

func (w *otherLogWatcher) start() {
	go func() {
		defer close(w.doneCh)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()

		signaled := false
		for {
			select {
			case <-w.stopCh:
				return
			case <-ticker.C:
				if !signaled && scanLogForFatal(w.path, &w.offset) {
					signaled = true
					if w.onFatal != nil {
						w.onFatal()
					}
				}
			}
		}
	}()
}

func (w *otherLogWatcher) stop() {
	w.stopOnce.Do(func() {
		close(w.stopCh)
		<-w.doneCh
	})
}
