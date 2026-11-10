//go:build linux

package packetcap

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"syscall"
	"unsafe"
)

const (
	ethPAll        = 0x0003
	soAttachFilter = 26
)

// Reader owns one non-blocking AF_PACKET socket bound to a single LAN bridge.
type Reader struct {
	mu    sync.Mutex
	fd    int
	valid bool

	readMu sync.Mutex
	buf    []byte
}

func htons(value uint16) uint16 { return value<<8 | value>>8 }

func rawSocket() (int, error) {
	// Protocol zero keeps the socket inactive until Open has attached BPF and
	// binds ETH_P_ALL to one LAN interface. No unfiltered setup window exists.
	return syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, 0)
}

// Probe checks socket creation and classic-BPF attachment without binding an
// interface or reading traffic. Open remains authoritative for CAP_NET_RAW and
// per-interface availability.
func Probe() error {
	fd, err := rawSocket()
	if err != nil {
		return fmt.Errorf("packetcap probe socket: %w", err)
	}
	defer syscall.Close(fd)
	if err := attachFilter(fd); err != nil {
		return fmt.Errorf("packetcap probe BPF: %w", err)
	}
	return nil
}

// Open attaches the restrictive filter before bind, so unselected normal
// traffic is never copied to this socket's receive queue.
func Open(cfg Config) (*Reader, error) {
	if cfg.Interface == "" {
		return nil, errors.New("packetcap: empty LAN interface")
	}
	if cfg.ReadBuffer == 0 {
		cfg.ReadBuffer = DefaultReadBuffer
	}
	if cfg.SocketBuffer == 0 {
		cfg.SocketBuffer = DefaultSocketBuffer
	}
	if cfg.ReadBuffer < 2048 || cfg.SocketBuffer < 0 {
		return nil, errors.New("packetcap: invalid receive buffer configuration")
	}
	iface, err := net.InterfaceByName(cfg.Interface)
	if err != nil {
		return nil, fmt.Errorf("packetcap interface %s: %w", cfg.Interface, err)
	}
	fd, err := rawSocket()
	if err != nil {
		return nil, fmt.Errorf("packetcap socket %s: %w", cfg.Interface, err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = syscall.Close(fd)
		}
	}()
	if err := attachFilter(fd); err != nil {
		return nil, fmt.Errorf("packetcap BPF %s: %w", cfg.Interface, err)
	}
	if err := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_RCVBUFFORCE, cfg.SocketBuffer); err != nil {
		if fallback := syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_RCVBUF, cfg.SocketBuffer); fallback != nil {
			return nil, fmt.Errorf("packetcap receive buffer %s: %w", cfg.Interface, fallback)
		}
	}
	addr := &syscall.SockaddrLinklayer{Protocol: htons(ethPAll), Ifindex: iface.Index}
	if err := syscall.Bind(fd, addr); err != nil {
		return nil, fmt.Errorf("packetcap bind %s: %w", cfg.Interface, err)
	}
	if err := syscall.SetNonblock(fd, true); err != nil {
		return nil, fmt.Errorf("packetcap nonblocking %s: %w", cfg.Interface, err)
	}
	keep = true
	return &Reader{fd: fd, valid: true, buf: make([]byte, cfg.ReadBuffer)}, nil
}

func attachFilter(fd int) error {
	instructions, err := KernelFilter()
	if err != nil {
		return err
	}
	filter := make([]syscall.SockFilter, len(instructions))
	for i, instruction := range instructions {
		filter[i] = syscall.SockFilter{
			Code: instruction.Code, Jt: instruction.Jt, Jf: instruction.Jf, K: instruction.K,
		}
	}
	program := &syscall.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	_, _, errno := syscall.Syscall6(
		syscall.SYS_SETSOCKOPT, uintptr(fd), uintptr(syscall.SOL_SOCKET), uintptr(soAttachFilter),
		uintptr(unsafe.Pointer(program)), unsafe.Sizeof(*program), 0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}

// FD returns -1 after Close. The caller must not close the descriptor itself.
func (r *Reader) FD() int {
	if r == nil {
		return -1
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.valid {
		return -1
	}
	return r.fd
}

// Read receives at most one frame and yields its IP bytes. Payload borrows the
// reader's buffer until yield returns. EAGAIN/EWOULDBLOCK means no frame ready.
func (r *Reader) Read(yield func([]byte)) error {
	if yield == nil {
		return errors.New("packetcap: nil payload callback")
	}
	if r == nil {
		return net.ErrClosed
	}
	r.readMu.Lock()
	defer r.readMu.Unlock()
	r.mu.Lock()
	if !r.valid {
		r.mu.Unlock()
		return net.ErrClosed
	}
	fd := r.fd
	r.mu.Unlock()

	n, _, err := syscall.Recvfrom(fd, r.buf, syscall.MSG_DONTWAIT|syscall.MSG_TRUNC)
	if err != nil {
		return err
	}
	truncated := n > len(r.buf)
	if truncated {
		n = len(r.buf)
	}
	payload, err := ParseFrame(r.buf[:n])
	if err != nil {
		return err
	}
	yield(payload)
	if truncated {
		return ErrTruncated
	}
	return nil
}

// Close is idempotent and releases the interface subscription and filter.
func (r *Reader) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.valid {
		return nil
	}
	fd := r.fd
	r.fd, r.valid = -1, false
	return syscall.Close(fd)
}
