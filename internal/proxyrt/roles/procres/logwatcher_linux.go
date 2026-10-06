//go:build linux

package procres

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

type linuxLogWatcher struct {
	path     string
	onNotify func()
	stopPipe [2]int
	doneCh   chan struct{}
	stopOnce sync.Once
}

func newPlatformLogWatcher(path string, onNotify func()) logWatcher {
	return &linuxLogWatcher{
		path:     path,
		onNotify: onNotify,
		doneCh:   make(chan struct{}),
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
	base := filepath.Base(w.path)

	// Ставим IN_CREATE ТОЛЬКО на каталог (без IN_MODIFY), чтобы модификация
	// соседних файлов других инстансов в RuntimeDir не будила этот watcher.
	dirWd, _ := unix.InotifyAddWatch(inotifyFd, dir, unix.IN_CREATE)
	fileWd := -1
	if _, statErr := os.Stat(w.path); statErr == nil {
		if wd, err := unix.InotifyAddWatch(inotifyFd, w.path, unix.IN_MODIFY); err == nil {
			fileWd = wd
		}
	}

	if w.onNotify != nil {
		w.onNotify()
	}

	go func() {
		defer close(w.doneCh)
		defer func() {
			_ = unix.Close(inotifyFd)
		}()

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
				needNotify := false
				for {
					rn, rerr := unix.Read(inotifyFd, buf[:])
					if rerr != nil || rn <= 0 {
						break
					}
					const header = 16
					for off := 0; off+header <= rn; {
						wd := int32(binary.NativeEndian.Uint32(buf[off : off+4]))
						mask := binary.NativeEndian.Uint32(buf[off+4 : off+8])
						nameLen := int(binary.NativeEndian.Uint32(buf[off+12 : off+16]))
						var name string
						if nameLen > 0 && off+header+nameLen <= rn {
							rawName := buf[off+header : off+header+nameLen]
							if idx := bytes.IndexByte(rawName, 0); idx >= 0 {
								name = string(rawName[:idx])
							} else {
								name = string(rawName)
							}
						}
						if wd == int32(dirWd) && mask&unix.IN_CREATE != 0 {
							if name == base {
								if nwd, err := unix.InotifyAddWatch(inotifyFd, w.path, unix.IN_MODIFY); err == nil {
									fileWd = nwd
									needNotify = true
								}
							}
						} else if fileWd >= 0 && wd == int32(fileWd) && mask&unix.IN_MODIFY != 0 {
							needNotify = true
						}
						off += header + nameLen
					}
				}

				if needNotify && w.onNotify != nil {
					w.onNotify()
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
