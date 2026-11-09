//go:build linux

package packetruntime

import (
	"context"
	"fmt"
	"syscall"
	"time"
)

// pollFDs uses select with a bounded timeout. AF_PACKET descriptors are
// non-blocking; no reader spins or waits indefinitely in Recvfrom.
func pollFDs(ctx context.Context, fds []int, timeout time.Duration) ([]bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ready := make([]bool, len(fds))
	var set syscall.FdSet
	maxFD := -1
	for _, fd := range fds {
		if fd < 0 {
			continue
		}
		word, bit := fd/64, uint(fd%64)
		if word >= len(set.Bits) {
			return nil, fmt.Errorf("packetruntime: descriptor %d exceeds select capacity", fd)
		}
		set.Bits[word] |= int64(1) << bit
		if fd > maxFD {
			maxFD = fd
		}
	}
	if maxFD < 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return ready, nil
		}
	}
	tv := syscall.NsecToTimeval(timeout.Nanoseconds())
	_, err := syscall.Select(maxFD+1, &set, nil, nil, &tv)
	if err == syscall.EINTR {
		return ready, nil
	}
	if err != nil {
		return nil, err
	}
	for i, fd := range fds {
		if fd >= 0 {
			ready[i] = set.Bits[fd/64]&(int64(1)<<uint(fd%64)) != 0
		}
	}
	return ready, nil
}
