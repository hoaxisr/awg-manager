//go:build linux

package procport

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// LookupListener finds the process bound to host:port (TCP LISTEN or UDP bind).
func LookupListener(host string, port int, proto Proto) (ListenerInfo, error) {
	info := ListenerInfo{Host: host, Port: port, Proto: string(proto)}
	if port <= 0 || port > 65535 {
		return info, errInvalidPort
	}
	wantPort := fmt.Sprintf("%04X", port)
	wantAddrs := make(map[string]struct{})
	for _, hex := range ipv4HexForms(host) {
		wantAddrs[hex+":"+wantPort] = struct{}{}
	}
	if len(wantAddrs) == 0 {
		return info, nil
	}

	inodes := listenerInodes(proto, wantAddrs)
	if len(inodes) == 0 {
		return info, nil
	}
	info.Open = true
	pid, ok := findListenerPIDByInodes(inodes)
	if !ok {
		return info, nil
	}
	info.PID = pid
	info.Comm = readComm(pid)
	return info, nil
}

// KillListener terminates the process owning host:port (SIGTERM → SIGKILL).
func KillListener(host string, port int, proto Proto) (ListenerInfo, error) {
	info, err := LookupListener(host, port, proto)
	if err != nil {
		return info, err
	}
	if !info.Open || info.PID <= 0 {
		return info, fmt.Errorf("порт %s:%d (%s) не занят процессом", host, port, proto)
	}
	if err := terminatePID(info.PID); err != nil {
		return info, err
	}
	after, _ := LookupListener(host, port, proto)
	if after.Open && after.PID > 0 {
		return info, fmt.Errorf("процесс %d не освободил порт после SIGTERM/SIGKILL", info.PID)
	}
	return info, nil
}

func listenerInodes(proto Proto, wantAddrs map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{})
	switch proto {
	case ProtoTCP:
		for _, procFile := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
			collectTCPListenerInodes(procFile, wantAddrs, out)
		}
	default:
		for _, procFile := range []string{"/proc/net/udp", "/proc/net/udp6"} {
			collectUDPBoundInodes(procFile, wantAddrs, out)
		}
	}
	return out
}

func collectTCPListenerInodes(procFile string, wantAddrs map[string]struct{}, out map[string]struct{}) {
	f, err := os.Open(procFile)
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Scan() // header
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 10 {
			continue
		}
		if fields[3] != "0A" { // TCP_LISTEN
			continue
		}
		if _, ok := wantAddrs[fields[1]]; ok {
			out[fields[9]] = struct{}{}
		}
	}
}

func collectUDPBoundInodes(procFile string, wantAddrs map[string]struct{}, out map[string]struct{}) {
	f, err := os.Open(procFile)
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Scan() // header
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 10 {
			continue
		}
		if _, ok := wantAddrs[fields[1]]; ok {
			out[fields[9]] = struct{}{}
		}
	}
}

func findListenerPIDByInodes(inodes map[string]struct{}) (int, bool) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, false
	}
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(ent.Name())
		if err != nil {
			continue
		}
		if pidOwnsInodes(pid, inodes) {
			return pid, true
		}
	}
	return 0, false
}

func pidOwnsInodes(pid int, inodes map[string]struct{}) bool {
	fdDir := filepath.Join("/proc", strconv.Itoa(pid), "fd")
	fds, err := os.ReadDir(fdDir)
	if err != nil {
		return false
	}
	for _, fd := range fds {
		link, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
		if err != nil {
			continue
		}
		if !strings.HasPrefix(link, "socket:[") {
			continue
		}
		inode := strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")
		if _, ok := inodes[inode]; ok {
			return true
		}
	}
	return false
}

func readComm(pid int) string {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "comm"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func ipv4HexForms(host string) []string {
	if host == "localhost" {
		host = "127.0.0.1"
	}
	parts := strings.Split(host, ".")
	if len(parts) != 4 {
		return nil
	}
	var b [4]byte
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 255 {
			return nil
		}
		b[i] = byte(n)
	}
	return []string{
		fmt.Sprintf("%02X%02X%02X%02X", b[3], b[2], b[1], b[0]),
		fmt.Sprintf("%02X%02X%02X%02X", b[0], b[1], b[2], b[3]),
	}
}
