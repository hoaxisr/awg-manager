//go:build linux

package captcha

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

const portCacheTTL = 3 * time.Second

var portCache struct {
	mu      sync.Mutex
	until   time.Time
	port    int
	open    bool
	owner   int
	pidsKey string
}

// socketListenerPIDAmong resolves the listener PID by scanning only candidatePIDs
// when provided. Falls back to a full /proc scan only if the port is open but
// none of the candidates own it (rare).
func socketListenerPIDAmong(host string, port int, candidatePIDs []int) (int, bool) {
	key := pidsCacheKey(candidatePIDs)
	if pid, open, ok := portCacheGet(port, key); ok {
		return pid, open
	}

	inodes := listenerInodes(host, port)
	if len(inodes) == 0 {
		portCacheSet(port, key, 0, false)
		return 0, false
	}

	for _, pid := range candidatePIDs {
		if pid <= 0 {
			continue
		}
		if pidOwnsInodes(pid, inodes) {
			portCacheSet(port, key, pid, true)
			return pid, true
		}
	}

	if len(candidatePIDs) == 0 {
		pid, ok := findListenerPIDByInodes(inodes)
		portCacheSet(port, key, pid, ok)
		return pid, ok
	}

	// Port is open but not owned by a known freeturn client PID.
	portCacheSet(port, key, 0, true)
	return 0, true
}

func listenerInodes(host string, port int) map[string]struct{} {
	wantPort := fmt.Sprintf("%04X", port)
	wantAddrs := make(map[string]struct{})
	for _, hex := range ipv4HexForms(host) {
		wantAddrs[hex+":"+wantPort] = struct{}{}
	}
	out := make(map[string]struct{})
	for _, procFile := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		collectListenerInodes(procFile, wantAddrs, out)
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

func portCacheGet(port int, pidsKey string) (pid int, open bool, ok bool) {
	portCache.mu.Lock()
	defer portCache.mu.Unlock()
	if time.Now().Before(portCache.until) && portCache.port == port && portCache.pidsKey == pidsKey {
		return portCache.owner, portCache.open, true
	}
	return 0, false, false
}

func portCacheSet(port int, pidsKey string, owner int, open bool) {
	portCache.mu.Lock()
	defer portCache.mu.Unlock()
	portCache.port = port
	portCache.pidsKey = pidsKey
	portCache.owner = owner
	portCache.open = open
	portCache.until = time.Now().Add(portCacheTTL)
}

func collectListenerInodes(procFile string, wantAddrs map[string]struct{}, out map[string]struct{}) {
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
		if _, ok := wantAddrs[local]; ok {
			out[fields[9]] = struct{}{}
		}
	}
}

// ipv4HexForms returns both /proc/net/tcp representations of an IPv4 address:
// the kernel prints the raw __be32 with %08X, so little-endian hosts (mipsel,
// aarch64) show 127.0.0.1 as 0100007F while big-endian mips shows 7F000001.
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
