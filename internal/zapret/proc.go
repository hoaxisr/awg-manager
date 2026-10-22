package zapret

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// nfqProcessesHook is a seam for resolver tests. Production reads /proc;
// tests can model more than one daemon without launching one.
var nfqProcessesHook = nfqProcesses

// nfqProc — найденный процесс демона zapret.
type nfqProc struct {
	pid int
	exe string
}

// nfqProcesses ищет процессы демона zapret через /proc.
//
// Pidfile читать не нужно: имя инстанса различается между форками
// (nfqws2.pid, nfqws2_1.pid), а KZM2 не пишет pidfile вовсе и определяет
// живость через `pidof`. Листинг /proc дешевле запуска внешней команды и
// не зависит от соглашений конкретного форка.
//
// Имя процесса сверяется по comm, а принадлежность zapret — по пути к
// исполняемому файлу: одноимённый бинарь из чужого каталога мы считать
// своим не будем.
func nfqProcesses() []nfqProc {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []nfqProc
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		exe, ok := nfqExe(pid)
		if !ok {
			continue
		}
		out = append(out, nfqProc{pid: pid, exe: exe})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].pid < out[j].pid })
	return out
}

// nfqExe возвращает путь к исполняемому файлу, если процесс — демон
// zapret. comm обрезан ядром до 15 байт, поэтому сверяем по префиксу.
func nfqExe(pid int) (string, bool) {
	dir := filepath.Join("/proc", strconv.Itoa(pid))
	return nfqExeInDir(dir)
}

func nfqExeInDir(dir string) (string, bool) {
	comm, err := os.ReadFile(filepath.Join(dir, "comm"))
	if err != nil {
		return "", false
	}
	name := strings.ToLower(strings.TrimSpace(string(comm)))
	if !isNFQWSName(name) {
		return "", false
	}
	// A terminated daemon can retain its comm until its parent reaps it,
	// but it no longer has an executable and cannot process traffic. Do not
	// mistake a zombie (or a process disappearing mid-read) for an unknown
	// live owner and turn a successful stop into a selection conflict.
	status, statusErr := os.ReadFile(filepath.Join(dir, "status"))
	if os.IsNotExist(statusErr) {
		return "", false
	}
	for _, line := range strings.Split(string(status), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 1 && fields[0] == "State:" && (fields[1] == "Z" || fields[1] == "X") {
			return "", false
		}
	}
	exe, err := os.Readlink(filepath.Join(dir, "exe"))
	if err != nil {
		if _, statErr := os.Stat(dir); os.IsNotExist(statErr) {
			return "", false
		}
		// Симлинк недоступен: у процесса, запущенного не нами, прав
		// может не хватать. comm уже совпал — считаем своим, иначе
		// работающий демон висит как остановленный.
		return "", true
	}
	// Принадлежность проверяем по пути: /opt/zapret22/… и есть наш
	// демон, а одноимённый бинарь из /usr/local/bin — нет.
	if !strings.Contains(strings.ToLower(exe), "zapret") && !pathWithin(optRoot, exe) {
		return "", false
	}
	return exe, true
}

// isNFQWSName узнаёт демон zapret по имени процесса. Форки зовут его
// по-разному: zapret2 — nfqws2, zapret — nfqws, BSD-вариант — dvtws,
// прокси-режим v1 — tpws.
func isNFQWSName(comm string) bool {
	for _, p := range []string{"nfqws", "dvtws", "tpws"} {
		if strings.HasPrefix(comm, p) {
			return true
		}
	}
	return false
}

func runningPIDsForBase(base string) (bool, []int) {
	want := canonicalBase(base)
	var pids []int
	for _, proc := range nfqProcessesHook() {
		if proc.exe == "" {
			continue
		}
		if got := canonicalBase(baseOfExe(proc.exe)); got != "" && got == want {
			pids = append(pids, proc.pid)
		}
	}
	return len(pids) > 0, pids
}
