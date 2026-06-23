package singbox

import (
	"fmt"
	"net"
	"strconv"
)

// portBindable reports whether 127.0.0.1:port can currently be bound. A
// tunnel's mixed inbound listens on 127.0.0.1:listen_port, and a failed
// inbound bind is fatal for the WHOLE sing-box process — so a port held by
// an external process (issue #384) must be skipped before it reaches the
// config. var, not func, so tests can stub it.
//
// ponytail: TCP-only probe — the mixed inbound's TCP listener is the bind
// that conflicts. There is a small TOCTOU window between this probe and
// sing-box actually binding; an external process can grab the port in
// between. Accepted: the alternative (retry at process start) is far more
// invasive, and candidate ports here are by definition not held by our own
// sing-box, so the common "stale/foreign holder of 1080" case is covered.
var portBindable = func(port int) bool {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// allocBindableSlot asks alloc() for the next free slot, skipping any whose
// listen port (firstPort+idx) is already bound by an external process. A
// skipped slot is marked into reserved so the next alloc() call hands out a
// different one (both NextFreeIndex and nextFreeListenPortSlot treat reserved
// as a skip-set). Bounded by maxProxySlots attempts.
func allocBindableSlot(reserved map[int]bool, alloc func() (int, error)) (idx, port int, err error) {
	for attempt := 0; attempt < maxProxySlots; attempt++ {
		idx, err = alloc()
		if err != nil {
			return 0, 0, err
		}
		port = firstPort + idx
		if portBindable(port) {
			return idx, port, nil
		}
		reserved[idx] = true // externally occupied — skip and retry
	}
	return 0, 0, fmt.Errorf("no bindable listen port: ports %d-%d all occupied", firstPort, firstPort+maxProxySlots-1)
}
