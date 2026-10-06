//go:build linux

package procres

import (
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

type linuxLogWatcher struct {
	path     string
	offset   int64
	onFatal  func()
	stopPipe [2]int
	doneCh   chan struct{}
	stopOnce sync.Once
}

func newPlatformLogWatcher(path string, startOffset int64, onFatal func()) logWatcher {
	if startOffset < 0 {
		startOffset = 0
	}
	return &linuxLogWatcher{
		path:    path,
		offset:  startOffset,
		onFatal: onFatal,
		doneCh:  make(chan struct{}),
	}
}

func (w *linuxLogWatcher) start() {
	if err := unix.Pipe2(w.stopPipe[:], unix.O_CLOEXEC|unix.O_NONBLOCK); err != nil {
		close(w.doneCh)
		return
	}

	inotifyFd, err := unix.InotifyInit1(unix.IN_NONBLOCK | unix.IN_CLOEXEC)
	if err != nil {
		_ = unix.Close(w.stopPipe[0])
		_ = unix.Close(w.stopPipe[1])
		close(w.doneCh)
		return
	}

	dir := filepath.Dir(w.path)
	_, _ = unix.InotifyAddWatch(inotifyFd, dir, unix.IN_CREATE|unix.IN_MODIFY)
	if _, statErr := os.Stat(w.path); statErr == nil {
		_, _ = unix.InotifyAddWatch(inotifyFd, w.path, unix.IN_MODIFY)
	}

	go func() {
		defer close(w.doneCh)
		defer func() {
			_ = unix.Close(inotifyFd)
		}()

		signaled := false
		var buf [4096]byte

		for {
			pfd := []unix.PollFd{
				{Fd: int32(w.stopPipe[0]), Events: unix.POLLIN},
				{Fd: int32(inotifyFd), Events: unix.POLLIN},
			}

			n, err := unix.Poll(pfd, -1)
			if err != nil {
				if err == unix.EINTR {
					continue
				}
				return
			}
			if n <= 0 {
				continue
			}

			if pfd[0].Revents&unix.POLLIN != 0 {
				return
			}

			if pfd[1].Revents&unix.POLLIN != 0 {
				for {
					rn, rerr := unix.Read(inotifyFd, buf[:])
					if rerr != nil || rn <= 0 {
						break
					}
				}

				if _, statErr := os.Stat(w.path); statErr == nil {
					_, _ = unix.InotifyAddWatch(inotifyFd, w.path, unix.IN_MODIFY)
				}

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

func (w *linuxLogWatcher) stop() {
	w.stopOnce.Do(func() {
		if w.stopPipe[1] != 0 {
			var b [1]byte
			_, _ = unix.Write(w.stopPipe[1], b[:])
		}
		<-w.doneCh
		if w.stopPipe[0] != 0 {
			_ = unix.Close(w.stopPipe[0])
			_ = unix.Close(w.stopPipe[1])
		}
	})
}
