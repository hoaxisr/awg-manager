//go:build linux

package freeturn

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const captchaPortCacheTTL = 3 * time.Second

var captchaPortCache struct {
	mu      sync.Mutex
	until   time.Time
	port    int
	open    bool
	owner   int
	pidsKey string
}

// socketListenerPID returns the PID of a process listening on host:port, or false.
func socketListenerPID(host string, port int) (int, bool) {
	return socketListenerPIDAmong(host, port, nil)
}

// socketListenerPIDAmong resolves the listener PID by scanning only candidatePIDs
// when provided. Falls back to a full /proc scan only if the port is open but
// none of the candidates own it (rare).
func socketListenerPIDAmong(host string, port int, candidatePIDs []int) (int, bool) {
	key := pidsCacheKey(candidatePIDs)
	if pid, open, ok := captchaPortCacheGet(port, key); ok {
		return pid, open
	}

	inodes := listenerInodes(host, port)
	if len(inodes) == 0 {
		captchaPortCacheSet(port, key, 0, false)
		return 0, false
	}

	for _, pid := range candidatePIDs {
		if pid <= 0 {
			continue
		}
		if pidOwnsInodes(pid, inodes) {
			captchaPortCacheSet(port, key, pid, true)
			return pid, true
		}
	}

	if len(candidatePIDs) == 0 {
		pid, ok := findListenerPIDByInodes(inodes)
		captchaPortCacheSet(port, key, pid, ok)
		return pid, ok
	}

	// Port is open but not owned by a known freeturn client PID.
	captchaPortCacheSet(port, key, 0, true)
	return 0, true
}

func listenerInodes(host string, port int) map[string]struct{} {
	wantPort := fmt.Sprintf("%04X", port)
	wantAddr4 := ipv4Hex(host) + ":" + wantPort
	out := make(map[string]struct{})
	for _, procFile := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		collectListenerInodes(procFile, wantAddr4, out)
	}
	return out
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

func pidsCacheKey(pids []int) string {
	if len(pids) == 0 {
		return ""
	}
	b := strings.Builder{}
	for i, pid := range pids {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Itoa(pid))
	}
	return b.String()
}

func captchaPortCacheGet(port int, pidsKey string) (pid int, open bool, ok bool) {
	captchaPortCache.mu.Lock()
	defer captchaPortCache.mu.Unlock()
	if time.Now().Before(captchaPortCache.until) && captchaPortCache.port == port && captchaPortCache.pidsKey == pidsKey {
		return captchaPortCache.owner, captchaPortCache.open, true
	}
	return 0, false, false
}

func captchaPortCacheSet(port int, pidsKey string, owner int, open bool) {
	captchaPortCache.mu.Lock()
	defer captchaPortCache.mu.Unlock()
	captchaPortCache.port = port
	captchaPortCache.pidsKey = pidsKey
	captchaPortCache.owner = owner
	captchaPortCache.open = open
	captchaPortCache.until = time.Now().Add(captchaPortCacheTTL)
}

func collectListenerInodes(procFile, wantAddr4 string, out map[string]struct{}) {
	f, err := os.Open(procFile)
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Scan() // skip header
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 10 {
			continue
		}
		local := fields[1]
		state := fields[3]
		// 0A = TCP_LISTEN
		if state != "0A" {
			continue
		}
		if local == wantAddr4 {
			out[fields[9]] = struct{}{}
		}
	}
}

func ipv4Hex(host string) string {
	switch host {
	case "127.0.0.1", "localhost":
		return "0100007F"
	default:
		parts := strings.Split(host, ".")
		if len(parts) != 4 {
			return ""
		}
		var b [4]byte
		for i, p := range parts {
			n, err := strconv.Atoi(p)
			if err != nil || n < 0 || n > 255 {
				return ""
			}
			b[i] = byte(n)
		}
		return fmt.Sprintf("%02X%02X%02X%02X", b[3], b[2], b[1], b[0])
	}
}
