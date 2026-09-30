package procnet

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

var (
	// ErrUnresolvedSocketOwner is returned when a matching socket inode exists in procfs,
	// but the owning process cannot be proven (e.g. unreadable fd directory or no matching fd).
	ErrUnresolvedSocketOwner = errors.New("procnet: socket owner PID cannot be proven")

	// ErrMalformedSocketTable is returned when a procfs socket table row matching the target port is malformed.
	ErrMalformedSocketTable = errors.New("procnet: malformed socket table row")
)

// ListenerLookup holds the tri-state outcome of probing listening sockets in procfs.
type ListenerLookup struct {
	SocketFound bool
	SocketInode string
	PID         int
}

// IPToProcHex converts an IP address string to the Linux kernel /proc/net/tcp format (little-endian hex).
func IPToProcHex(addr string) string {
	ip := net.ParseIP(addr)
	if ip == nil {
		return ""
	}
	if ip4 := ip.To4(); ip4 != nil {
		return fmt.Sprintf("%02X%02X%02X%02X", ip4[3], ip4[2], ip4[1], ip4[0])
	}
	ip6 := ip.To16()
	if ip6 == nil {
		return ""
	}
	var b strings.Builder
	for i := 0; i < 16; i += 4 {
		fmt.Fprintf(&b, "%02X%02X%02X%02X", ip6[i+3], ip6[i+2], ip6[i+1], ip6[i])
	}
	return b.String()
}

// IsIPv6Target reports whether the address refers to an IPv6 destination.
func IsIPv6Target(addr string) bool {
	if addr == "" {
		return false
	}
	ip := net.ParseIP(addr)
	if ip != nil {
		return ip.To4() == nil
	}
	return strings.Contains(addr, ":")
}

type socketTableSpec struct {
	path      string
	wantState string
	isIPv6    bool
}

// FindListeningProcessNetwork scans procfs socket tables for an active listening socket
// matching network, addr, and port, and if found, resolves the owning PID from /proc/<pid>/fd.
// It supports "tcp", "tcp4", "tcp6", "udp", "udp4", "udp6".
// For TCP, state "0A" (TCP_LISTEN) is expected.
// For UDP, state "07" (UDP_UNCONN / bound) is expected.
// It fails closed on unknown network protocols, missing/unreadable mandatory tables, or directory socket tables.
func FindListeningProcessNetwork(procDir string, network string, addr string, port int) (ListenerLookup, error) {
	if procDir == "" {
		procDir = "/proc"
	}
	netLower := strings.ToLower(strings.TrimSpace(network))
	if netLower == "" {
		netLower = "tcp"
	}

	switch netLower {
	case "tcp", "tcp4", "tcp6", "udp", "udp4", "udp6":
		// supported
	default:
		return ListenerLookup{}, fmt.Errorf("unsupported network protocol: %q", network)
	}

	portHex := fmt.Sprintf("%04X", port)
	targetAddrHex := strings.ToUpper(IPToProcHex(addr))

	var specs []socketTableSpec

	switch netLower {
	case "tcp4":
		p := filepath.Join(procDir, "net", "tcp")
		info, err := os.Stat(p)
		if err != nil {
			return ListenerLookup{}, fmt.Errorf("ipv4 socket table %s is unavailable: %w", p, err)
		}
		if info.IsDir() {
			return ListenerLookup{}, fmt.Errorf("ipv4 socket table %s is a directory", p)
		}
		specs = append(specs, socketTableSpec{path: p, wantState: "0A", isIPv6: false})

	case "tcp6":
		p := filepath.Join(procDir, "net", "tcp6")
		info, err := os.Stat(p)
		if err != nil {
			return ListenerLookup{}, fmt.Errorf("ipv6 socket table %s is unavailable: %w", p, err)
		}
		if info.IsDir() {
			return ListenerLookup{}, fmt.Errorf("ipv6 socket table %s is a directory", p)
		}
		specs = append(specs, socketTableSpec{path: p, wantState: "0A", isIPv6: true})

	case "tcp":
		if IsIPv6Target(addr) {
			p := filepath.Join(procDir, "net", "tcp6")
			info, err := os.Stat(p)
			if err != nil {
				return ListenerLookup{}, fmt.Errorf("ipv6 socket table %s is unavailable: %w", p, err)
			}
			if info.IsDir() {
				return ListenerLookup{}, fmt.Errorf("ipv6 socket table %s is a directory", p)
			}
			specs = append(specs, socketTableSpec{path: p, wantState: "0A", isIPv6: true})
		} else {
			p := filepath.Join(procDir, "net", "tcp")
			info, err := os.Stat(p)
			if err != nil {
				return ListenerLookup{}, fmt.Errorf("ipv4 socket table %s is unavailable: %w", p, err)
			}
			if info.IsDir() {
				return ListenerLookup{}, fmt.Errorf("ipv4 socket table %s is a directory", p)
			}
			specs = append(specs, socketTableSpec{path: p, wantState: "0A", isIPv6: false})

			p6 := filepath.Join(procDir, "net", "tcp6")
			if info6, err := os.Stat(p6); err == nil {
				if info6.IsDir() {
					return ListenerLookup{}, fmt.Errorf("ipv6 socket table %s is a directory", p6)
				}
				specs = append(specs, socketTableSpec{path: p6, wantState: "0A", isIPv6: true})
			} else if !os.IsNotExist(err) {
				return ListenerLookup{}, fmt.Errorf("failed to access ipv6 socket table %s: %w", p6, err)
			}
		}

	case "udp4":
		p := filepath.Join(procDir, "net", "udp")
		info, err := os.Stat(p)
		if err != nil {
			return ListenerLookup{}, fmt.Errorf("ipv4 socket table %s is unavailable: %w", p, err)
		}
		if info.IsDir() {
			return ListenerLookup{}, fmt.Errorf("ipv4 socket table %s is a directory", p)
		}
		specs = append(specs, socketTableSpec{path: p, wantState: "07", isIPv6: false})

	case "udp6":
		p := filepath.Join(procDir, "net", "udp6")
		info, err := os.Stat(p)
		if err != nil {
			return ListenerLookup{}, fmt.Errorf("ipv6 socket table %s is unavailable: %w", p, err)
		}
		if info.IsDir() {
			return ListenerLookup{}, fmt.Errorf("ipv6 socket table %s is a directory", p)
		}
		specs = append(specs, socketTableSpec{path: p, wantState: "07", isIPv6: true})

	case "udp":
		if IsIPv6Target(addr) {
			p := filepath.Join(procDir, "net", "udp6")
			info, err := os.Stat(p)
			if err != nil {
				return ListenerLookup{}, fmt.Errorf("ipv6 socket table %s is unavailable: %w", p, err)
			}
			if info.IsDir() {
				return ListenerLookup{}, fmt.Errorf("ipv6 socket table %s is a directory", p)
			}
			specs = append(specs, socketTableSpec{path: p, wantState: "07", isIPv6: true})
		} else {
			p := filepath.Join(procDir, "net", "udp")
			info, err := os.Stat(p)
			if err != nil {
				return ListenerLookup{}, fmt.Errorf("ipv4 socket table %s is unavailable: %w", p, err)
			}
			if info.IsDir() {
				return ListenerLookup{}, fmt.Errorf("ipv4 socket table %s is a directory", p)
			}
			specs = append(specs, socketTableSpec{path: p, wantState: "07", isIPv6: false})

			p6 := filepath.Join(procDir, "net", "udp6")
			if info6, err := os.Stat(p6); err == nil {
				if info6.IsDir() {
					return ListenerLookup{}, fmt.Errorf("ipv6 socket table %s is a directory", p6)
				}
				specs = append(specs, socketTableSpec{path: p6, wantState: "07", isIPv6: true})
			} else if !os.IsNotExist(err) {
				return ListenerLookup{}, fmt.Errorf("failed to access ipv6 socket table %s: %w", p6, err)
			}
		}
	}

	var inodes []string
	socketFound := false

	for _, spec := range specs {
		data, err := os.ReadFile(spec.path)
		if err != nil {
			return ListenerLookup{}, fmt.Errorf("failed to read socket table %s: %w", spec.path, err)
		}
		for lineNum, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 0 || strings.HasPrefix(fields[0], "sl") {
				continue
			}
			if len(fields) < 10 {
				if strings.Contains(line, ":"+portHex) {
					return ListenerLookup{}, fmt.Errorf("%w: line %d in %s has %d fields (< 10) matching port %s", ErrMalformedSocketTable, lineNum+1, spec.path, len(fields), portHex)
				}
				continue
			}
			local := strings.ToUpper(fields[1])
			parts := strings.Split(local, ":")
			if len(parts) != 2 {
				if strings.Contains(local, portHex) {
					return ListenerLookup{}, fmt.Errorf("%w: line %d in %s has invalid local address %q", ErrMalformedSocketTable, lineNum+1, spec.path, local)
				}
				continue
			}
			if parts[1] != portHex {
				continue
			}
			if fields[3] != spec.wantState {
				continue
			}
			rowAddr := parts[0]
			isWildcardListener := strings.Trim(rowAddr, "0") == ""
			isWildcardTarget := addr == "" || addr == "0.0.0.0" || addr == "::"

			if spec.isIPv6 && !IsIPv6Target(addr) && !isWildcardTarget {
				// Target is a specific IPv4 target (e.g. 127.0.0.1).
				// Do not match IPv6 wildcard to a specific IPv4 target!
				if isWildcardListener {
					continue
				}
			}

			if isWildcardTarget || isWildcardListener || (targetAddrHex != "" && rowAddr == targetAddrHex) {
				socketFound = true
				inode := fields[9]
				if inode != "" && inode != "0" {
					inodes = append(inodes, inode)
				}
			}
		}
	}

	if !socketFound {
		return ListenerLookup{SocketFound: false, SocketInode: "", PID: 0}, nil
	}

	firstInode := ""
	if len(inodes) > 0 {
		firstInode = inodes[0]
	}

	if len(inodes) == 0 {
		return ListenerLookup{SocketFound: true, SocketInode: "", PID: 0},
			fmt.Errorf("%w: socket found for %s:%d (%s) but has empty inode", ErrUnresolvedSocketOwner, addr, port, network)
	}

	inodeMap := make(map[string]bool, len(inodes)*2)
	for _, in := range inodes {
		inodeMap[fmt.Sprintf("socket:[%s]", in)] = true
		inodeMap[in] = true
	}

	entries, err := os.ReadDir(procDir)
	if err != nil {
		return ListenerLookup{SocketFound: true, SocketInode: firstInode, PID: 0}, fmt.Errorf("failed to read proc directory %s: %w", procDir, err)
	}

	foundPIDs := make(map[int]bool)
	unreadableFDErrors := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		fdDir := filepath.Join(procDir, entry.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			unreadableFDErrors++
			continue
		}
		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil {
				unreadableFDErrors++
				continue
			}
			cleanLink := strings.TrimPrefix(strings.TrimSuffix(link, "]"), "socket:[")
			if inodeMap[link] || inodeMap[cleanLink] {
				foundPIDs[pid] = true
				break
			}
		}
	}

	if len(foundPIDs) > 1 {
		pids := make([]int, 0, len(foundPIDs))
		for p := range foundPIDs {
			pids = append(pids, p)
		}
		sort.Ints(pids)
		return ListenerLookup{SocketFound: true, SocketInode: firstInode, PID: 0},
			fmt.Errorf("ambiguous socket ownership for %s:%d (%s): multiple owning PIDs: %v", addr, port, network, pids)
	}

	if len(foundPIDs) == 1 {
		for p := range foundPIDs {
			return ListenerLookup{
				SocketFound: true,
				SocketInode: firstInode,
				PID:         p,
			}, nil
		}
	}

	if unreadableFDErrors > 0 {
		return ListenerLookup{SocketFound: true, SocketInode: firstInode, PID: 0},
			fmt.Errorf("%w: socket inode %s found for %s:%d (%s) but %d fd entries were unreadable", ErrUnresolvedSocketOwner, firstInode, addr, port, network, unreadableFDErrors)
	}

	return ListenerLookup{
		SocketFound: true,
		SocketInode: firstInode,
		PID:         0,
	}, fmt.Errorf("%w: socket inode %s found for %s:%d (%s) but no process fd references it", ErrUnresolvedSocketOwner, firstInode, addr, port, network)
}

// FindListeningProcess scans procfs socket tables for an active TCP listening socket.
// It is preserved as a backward-compatible TCP wrapper.
func FindListeningProcess(procDir string, addr string, port int) (ListenerLookup, error) {
	return FindListeningProcessNetwork(procDir, "tcp", addr, port)
}

// FindListeningPIDForAddressPort returns the PID listening on addr:port, or 0 if absent/unresolved.
func FindListeningPIDForAddressPort(procDir string, addr string, port int) int {
	lookup, err := FindListeningProcess(procDir, addr, port)
	if err != nil || !lookup.SocketFound {
		return 0
	}
	return lookup.PID
}

// FindListeningPIDForPort returns the PID listening on port on any interface, or 0 if absent/unresolved.
func FindListeningPIDForPort(port int) int {
	return FindListeningPIDForAddressPort("/proc", "", port)
}
