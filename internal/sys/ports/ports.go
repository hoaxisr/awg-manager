package ports

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/hoaxisr/awg-manager/internal/sys/procmon"
)

// Binding describes an open/listening network port and the associated process.
type Binding struct {
	Proto       string `json:"proto"`                 // "tcp", "udp", "tcp6", "udp6"
	Port        int    `json:"port"`                  // Port number (e.g. 56013)
	IP          string `json:"ip"`                    // Local IP (e.g. "0.0.0.0", "127.0.0.1", "::")
	State       string `json:"state"`                 // "LISTEN", "UNCONN", etc.
	Inode       uint64 `json:"inode"`                 // Socket inode
	PID         int    `json:"pid,omitempty"`         // Process ID
	ProcessName string `json:"processName,omitempty"` // Name (e.g. "wt-client", "dropbear")
	Exe         string `json:"exe,omitempty"`         // Binary path (e.g. "/opt/bin/wt-client")
	Cmdline     string `json:"cmdline,omitempty"`     // Full command line arguments
	User        string `json:"user,omitempty"`        // User / UID
	Service     string `json:"service,omitempty"`     // Associated init.d service (if detected)
	IsSelf      bool   `json:"isSelf,omitempty"`      // True if it is this awg-manager daemon
	IsCritical  bool   `json:"isCritical,omitempty"`  // True if terminating may affect connectivity
}

// Scanner collects socket and process information from /proc.
type Scanner struct {
	procDir string
	initDir string
}

// NewScanner creates a new ports Scanner.
func NewScanner() *Scanner {
	return &Scanner{
		procDir: "/proc",
		initDir: "/opt/etc/init.d",
	}
}

// List returns all listening TCP and bound UDP ports.
func (s *Scanner) List() ([]Binding, error) {
	procDir := s.procDir
	if procDir == "" {
		procDir = "/proc"
	}

	var bindings []Binding

	// 1. Collect sockets from /proc/net/{tcp,tcp6,udp,udp6}
	tcp4, _ := s.parseNetFile(filepath.Join(procDir, "net", "tcp"), "tcp", true)
	tcp6, _ := s.parseNetFile(filepath.Join(procDir, "net", "tcp6"), "tcp6", true)
	udp4, _ := s.parseNetFile(filepath.Join(procDir, "net", "udp"), "udp", false)
	udp6, _ := s.parseNetFile(filepath.Join(procDir, "net", "udp6"), "udp6", false)

	bindings = append(bindings, tcp4...)
	bindings = append(bindings, tcp6...)
	bindings = append(bindings, udp4...)
	bindings = append(bindings, udp6...)

	if len(bindings) == 0 {
		return []Binding{}, nil
	}

	// 2. Map inode -> *Binding
	inodeMap := make(map[uint64][]*Binding, len(bindings))
	for i := range bindings {
		b := &bindings[i]
		if b.Inode > 0 {
			inodeMap[b.Inode] = append(inodeMap[b.Inode], b)
		}
	}

	// 3. Scan /proc/[pid]/fd to associate sockets with processes
	s.enrichWithProcesses(procDir, inodeMap)

	// 4. Sort by Port ASC, Proto ASC, IP ASC
	sort.Slice(bindings, func(i, j int) bool {
		if bindings[i].Port != bindings[j].Port {
			return bindings[i].Port < bindings[j].Port
		}
		if bindings[i].Proto != bindings[j].Proto {
			return bindings[i].Proto < bindings[j].Proto
		}
		return bindings[i].IP < bindings[j].IP
	})

	return bindings, nil
}

// InspectPort returns bindings matching a specific port number and optional proto.
func (s *Scanner) InspectPort(port int, proto string) ([]Binding, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	proto = strings.ToLower(strings.TrimSpace(proto))
	var matches []Binding
	for _, b := range all {
		if b.Port == port {
			if proto == "" || strings.HasPrefix(b.Proto, proto) {
				matches = append(matches, b)
			}
		}
	}
	return matches, nil
}

// KillProcess terminates a process by PID with safety checks.
// Critical processes (init, ndm, dropbear, sshd, systemd, klogd, syslogd,
// awg-manager, kthreadd) cannot be killed via this API even if the caller
// supplies SIGKILL — the same predicate is shared with the process monitor
// (procmon.IsCriticalProcess) to keep both kill paths in sync.
func (s *Scanner) KillProcess(pid int, signalName string) error {
	if pid <= 1 {
		return fmt.Errorf("cannot kill system process with PID %d", pid)
	}

	selfPid := os.Getpid()
	if pid == selfPid {
		return fmt.Errorf("cannot kill self (awg-manager process PID %d)", pid)
	}

	comm, exe := s.readProcCommPID(pid), s.readProcExePID(pid)
	if procmon.IsCriticalProcess(comm, exe) {
		return fmt.Errorf("cannot kill critical process %q (PID %d)", comm, pid)
	}

	sig := syscall.SIGTERM
	if strings.ToUpper(strings.TrimSpace(signalName)) == "SIGKILL" || strings.ToUpper(strings.TrimSpace(signalName)) == "KILL" || strings.ToUpper(strings.TrimSpace(signalName)) == "9" {
		sig = syscall.SIGKILL
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}

	if err := proc.Signal(sig); err != nil {
		return fmt.Errorf("signal %s to PID %d: %w", sig, pid, err)
	}

	return nil
}

// parseNetFile parses /proc/net/{tcp,tcp6,udp,udp6}
func (s *Scanner) parseNetFile(filePath, proto string, listenOnly bool) ([]Binding, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var result []Binding
	scanner := bufio.NewScanner(f)
	isFirst := true

	for scanner.Scan() {
		if isFirst {
			isFirst = false
			continue // skip header line
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}

		// local_address is field 1: e.g. "00000000:0016" or "0100007F:15E0"
		localAddr := fields[1]
		parts := strings.Split(localAddr, ":")
		if len(parts) != 2 {
			continue
		}

		port64, err := strconv.ParseUint(parts[1], 16, 16)
		if err != nil {
			continue
		}
		port := int(port64)

		var ip string
		if len(parts[0]) == 8 {
			ip, err = parseHexIPv4(parts[0])
		} else if len(parts[0]) == 32 {
			ip, err = parseHexIPv6(parts[0])
		} else {
			continue
		}
		if err != nil {
			continue
		}

		// State is field 3: "0A" = LISTEN for TCP, "07" = CLOSE/DEFAULT for UDP
		stateHex := fields[3]
		state := decodeSocketState(stateHex, proto)

		// For TCP, only return LISTEN sockets. Connected TCP sockets are client streams.
		if strings.HasPrefix(proto, "tcp") && listenOnly && stateHex != "0A" {
			continue
		}

		// inode is field 9
		inode, err := strconv.ParseUint(fields[9], 10, 64)
		if err != nil {
			inode = 0
		}

		uid := fields[7]

		result = append(result, Binding{
			Proto: proto,
			Port:  port,
			IP:    ip,
			State: state,
			Inode: inode,
			User:  uidToName(uid),
		})
	}

	return result, scanner.Err()
}

func decodeSocketState(st, proto string) string {
	if strings.HasPrefix(proto, "udp") {
		return "LISTEN"
	}
	switch strings.ToUpper(st) {
	case "0A":
		return "LISTEN"
	case "01":
		return "ESTABLISHED"
	case "02":
		return "SYN_SENT"
	case "03":
		return "SYN_RECV"
	case "04":
		return "FIN_WAIT1"
	case "05":
		return "FIN_WAIT2"
	case "06":
		return "TIME_WAIT"
	case "07":
		return "CLOSE"
	case "08":
		return "CLOSE_WAIT"
	case "09":
		return "LAST_ACK"
	case "0B":
		return "CLOSING"
	default:
		return st
	}
}

func parseHexIPv4(hexStr string) (string, error) {
	if len(hexStr) != 8 {
		return "", fmt.Errorf("invalid ipv4 hex length: %d", len(hexStr))
	}
	val, err := strconv.ParseUint(hexStr, 16, 32)
	if err != nil {
		return "", err
	}
	ip := net.IPv4(byte(val), byte(val>>8), byte(val>>16), byte(val>>24))
	return ip.String(), nil
}

func parseHexIPv6(hexStr string) (string, error) {
	if len(hexStr) != 32 {
		return "", fmt.Errorf("invalid ipv6 hex length: %d", len(hexStr))
	}
	var ip net.IP = make([]byte, 16)
	for i := 0; i < 4; i++ {
		val, err := strconv.ParseUint(hexStr[i*8:(i+1)*8], 16, 32)
		if err != nil {
			return "", err
		}
		ip[i*4] = byte(val)
		ip[i*4+1] = byte(val >> 8)
		ip[i*4+2] = byte(val >> 16)
		ip[i*4+3] = byte(val >> 24)
	}
	return ip.String(), nil
}

func (s *Scanner) enrichWithProcesses(procDir string, inodeMap map[uint64][]*Binding) {
	entries, err := os.ReadDir(procDir)
	if err != nil {
		return
	}

	selfPid := os.Getpid()
	initServices := s.scanInitServices()

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}

		fdDir := filepath.Join(procDir, entry.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}

		for _, fd := range fds {
			link, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil {
				continue
			}

			// socket:[12345] or [0000]:12345
			var inode uint64
			if strings.HasPrefix(link, "socket:[") && strings.HasSuffix(link, "]") {
				raw := strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")
				inode, _ = strconv.ParseUint(raw, 10, 64)
			} else if strings.HasPrefix(link, "[0000]:") {
				raw := strings.TrimPrefix(link, "[0000]:")
				inode, _ = strconv.ParseUint(raw, 10, 64)
			}

			if inode == 0 {
				continue
			}

			if targets, ok := inodeMap[inode]; ok {
				comm := s.readProcComm(procDir, pid)
				exe := s.readProcExe(procDir, pid)
				cmdline := s.readProcCmdline(procDir, pid)
				isSelf := (pid == selfPid)
				isCritical := procmon.IsCriticalProcess(comm, exe)
				service := matchService(exe, comm, initServices)

				for _, t := range targets {
					t.PID = pid
					t.ProcessName = comm
					t.Exe = exe
					t.Cmdline = cmdline
					t.IsSelf = isSelf
					t.IsCritical = isCritical
					t.Service = service
				}
			}
		}
	}
}

func (s *Scanner) readProcComm(procDir string, pid int) string {
	data, err := os.ReadFile(filepath.Join(procDir, strconv.Itoa(pid), "comm"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func (s *Scanner) readProcCommPID(pid int) string {
	return s.readProcComm("/proc", pid)
}

func (s *Scanner) readProcExe(procDir string, pid int) string {
	link, err := os.Readlink(filepath.Join(procDir, strconv.Itoa(pid), "exe"))
	if err != nil {
		return ""
	}
	return link
}

func (s *Scanner) readProcExePID(pid int) string {
	return s.readProcExe("/proc", pid)
}

func (s *Scanner) readProcCmdline(procDir string, pid int) string {
	data, err := os.ReadFile(filepath.Join(procDir, strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return ""
	}
	parts := strings.Split(string(data), "\x00")
	var nonClean []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			nonClean = append(nonClean, p)
		}
	}
	return strings.Join(nonClean, " ")
}

func (s *Scanner) scanInitServices() map[string]string {
	res := make(map[string]string)
	dir := s.initDir
	if dir == "" {
		dir = "/opt/etc/init.d"
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return res
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if len(name) > 3 && name[0] == 'S' {
			svcName := name[3:]
			res[svcName] = name
		}
	}
	return res
}

func matchService(exe, comm string, services map[string]string) string {
	baseExe := filepath.Base(exe)
	if s, ok := services[baseExe]; ok {
		return s
	}
	if s, ok := services[comm]; ok {
		return s
	}
	return ""
}

func uidToName(uid string) string {
	if uid == "0" {
		return "root"
	}
	return uid
}
