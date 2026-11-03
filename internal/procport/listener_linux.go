//go:build linux

package procport

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// procRoot — корень procfs; шов для тестов, которые собирают поддельный /proc.
var procRoot = "/proc"

// LookupListener finds the process bound to host:port (TCP LISTEN or UDP bind).
func LookupListener(host string, port int, proto Proto) (ListenerInfo, error) {
	info := ListenerInfo{Host: host, Port: port, Proto: string(proto)}
	if port <= 0 || port > 65535 {
		return info, errInvalidPort
	}
	want, ok := newAddrMatch(host, port)
	if !ok {
		return info, nil
	}

	inodes := listenerInodes(proto, want)
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
//
// host — адрес, на который делает bind управляемый инстанс (берётся из его
// конфига, не из запроса): цель — только сокет, конфликтующий с этим bind.
// Любая неоднозначность — отказ без сигнала.
func KillListener(host string, port int, proto Proto) (ListenerInfo, error) {
	info := ListenerInfo{Host: host, Port: port, Proto: string(proto)}
	if port <= 0 || port > 65535 {
		return info, errInvalidPort
	}
	free := fmt.Errorf("порт %s:%d (%s) не занят процессом", host, port, proto)
	want, ok := newAddrMatch(host, port)
	if !ok {
		return info, free
	}
	inodes := listenerInodes(proto, want)
	if len(inodes) == 0 {
		return info, free
	}
	info.Open = true
	pids, err := listenerPIDs(inodes)
	if err != nil {
		return info, err
	}
	if len(pids) == 0 {
		return info, free
	}
	// Себя и init не трогаем ни при каких настройках инстанса. Проверяются все
	// держатели сокета: пока его держит защищённый процесс, остановка соседа
	// порт не освободит.
	for _, pid := range pids {
		if pid == os.Getpid() || pid == 1 {
			info.PID, info.Comm = pid, readComm(pid)
			return info, fmt.Errorf("процесс %d (%s) остановке не подлежит", info.PID, info.Comm)
		}
	}
	info.PID = pids[0]
	info.Comm = readComm(info.PID)
	start, ok := processStartTime(info.PID)
	if !ok {
		return info, fmt.Errorf("процесс %d не удалось опознать — сигнал не отправлен", info.PID)
	}
	target := killTarget{pid: info.PID, start: start}
	if !target.holdsListener(proto, want) {
		return info, fmt.Errorf("процесс %d сменился или освободил порт — сигнал не отправлен", info.PID)
	}
	if err := terminatePID(target); err != nil {
		return info, err
	}
	after, _ := LookupListener(host, port, proto)
	if after.Open && after.PID > 0 {
		return info, fmt.Errorf("процесс %d не освободил порт после SIGTERM/SIGKILL", info.PID)
	}
	return info, nil
}

func listenerInodes(proto Proto, want addrMatch) map[string]struct{} {
	out := make(map[string]struct{})
	files := []string{"udp", "udp6"}
	if proto == ProtoTCP {
		files = []string{"tcp", "tcp6"}
	}
	for _, procFile := range files {
		f, err := os.Open(filepath.Join(procRoot, "net", procFile))
		if err != nil {
			continue
		}
		collectListenerInodes(f, proto, want, out)
		f.Close()
	}
	return out
}

// collectListenerInodes сканирует таблицу /proc/net/{tcp,udp}[6] и складывает
// inode'ы сокетов, занявших искомый порт.
func collectListenerInodes(r io.Reader, proto Proto, want addrMatch, out map[string]struct{}) {
	sc := bufio.NewScanner(r)
	sc.Scan() // header
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 10 {
			continue
		}
		if proto == ProtoTCP && fields[3] != "0A" { // TCP_LISTEN
			continue
		}
		if want.matches(fields[1]) {
			out[fields[9]] = struct{}{}
		}
	}
}

func findListenerPIDByInodes(inodes map[string]struct{}) (int, bool) {
	found := 0
	err := walkListenerPIDs(inodes, func(pid int) bool {
		found = pid
		return false
	})
	return found, err == nil && found > 0
}

// listenerPIDs — все процессы, держащие один из inodes (сокет наследуется
// при fork, держателей бывает несколько). Порядок — порядок обхода /proc.
func listenerPIDs(inodes map[string]struct{}) ([]int, error) {
	var pids []int
	err := walkListenerPIDs(inodes, func(pid int) bool {
		pids = append(pids, pid)
		return true
	})
	return pids, err
}

// walkListenerPIDs зовёт fn для каждого процесса, держащего один из inodes;
// fn=false останавливает обход.
func walkListenerPIDs(inodes map[string]struct{}, fn func(pid int) bool) error {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return err
	}
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(ent.Name())
		if err != nil {
			continue
		}
		if pidOwnsInodes(pid, inodes) && !fn(pid) {
			return nil
		}
	}
	return nil
}

func pidOwnsInodes(pid int, inodes map[string]struct{}) bool {
	fdDir := filepath.Join(procRoot, strconv.Itoa(pid), "fd")
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
	data, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "comm"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// addrMatch решает, занял ли сокет из /proc/net искомый host:port.
type addrMatch struct {
	portHex  string
	addrs    map[string]bool // допустимые формы адреса, пусто = любой
	anyLocal bool            // ищем 0.0.0.0: конфликтует любой адрес на порту
}

// newAddrMatch раскладывает host в формы, в которых его печатает ядро.
//
// /proc/net печатает адрес как %08X от нативных слов, поэтому порядок байт
// зависит от платформы: на little-endian (aarch64, mipsel) 127.0.0.1 — это
// "0100007F", на big-endian (mips) — "7F000001". Принимаем обе формы: лишний
// вариант в худшем случае совпадёт с экзотическим адресом, а пропуск означал бы
// «порт свободен» ровно там, где bind падает с EADDRINUSE.
//
// Для tcp6/udp6 тот же адрес приходит в v4-mapped виде (16 нулей + FFFF + ip),
// и слово FFFF тоже переставлено на LE — отсюда обе 32-символьные формы.
// Слушатель на wildcard (0.0.0.0 или ::) занимает порт для любого адреса, его
// ловит allZeroHex в matches.
func newAddrMatch(host string, port int) (addrMatch, bool) {
	m := addrMatch{portHex: fmt.Sprintf("%04X", port)}
	if host == "localhost" {
		host = "127.0.0.1"
	}
	if host == "0.0.0.0" || host == "::" || host == "" {
		m.anyLocal = true
		return m, true
	}
	parts := strings.Split(host, ".")
	if len(parts) != 4 {
		return m, false
	}
	var b [4]byte
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 255 {
			return m, false
		}
		b[i] = byte(n)
	}
	le := fmt.Sprintf("%02X%02X%02X%02X", b[3], b[2], b[1], b[0])
	be := fmt.Sprintf("%02X%02X%02X%02X", b[0], b[1], b[2], b[3])
	const zeros16 = "0000000000000000"
	m.addrs = map[string]bool{
		le:                        true,
		be:                        true,
		zeros16 + "FFFF0000" + le: true,
		zeros16 + "0000FFFF" + be: true,
	}
	return m, true
}

func (m addrMatch) matches(field string) bool {
	addr, port, ok := strings.Cut(field, ":")
	if !ok || !strings.EqualFold(port, m.portHex) {
		return false
	}
	addr = strings.ToUpper(addr)
	if m.anyLocal || allZeroHex(addr) {
		return true
	}
	return m.addrs[addr]
}

func allZeroHex(s string) bool {
	if s == "" {
		return false
	}
	return strings.Trim(s, "0") == ""
}
