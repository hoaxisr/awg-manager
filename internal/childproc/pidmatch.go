package childproc

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// MatchesBinary reports whether pid's /proc cmdline actually names binary.
//
// Pid-файлы freeturn/wdtt лежат в /opt/etc/awg-manager/run — на персистентной
// флешке, поэтому переживают перезагрузку роутера, а PID после ребута
// переиспользуется произвольным процессом. Без этой сверки IsRunning считает
// прокси живым (автостарт молча не срабатывает), а Stop шлёт SIGTERM/SIGKILL
// постороннему процессу. Тот же приём в internal/singbox.Process.matchesBinary.
func MatchesBinary(pid int, binary string) bool {
	if binary == "" {
		return true // тесты конструируют процесс без бинаря
	}
	base, ok := cmdlineArgv0Base(pid)
	if !ok {
		// Личность не подтверждается: pid исчез между kill(0) и чтением либо
		// cmdline недоступен, либо cmdline пуст. Fail-closed — считаем, что
		// это не наш процесс.
		//
		// Пустой cmdline — это НЕ только зомби. Те же несколько десятков
		// микросекунд он пуст у процесса, который ТОЛЬКО ЧТО прошёл execve:
		// ядро закрывает CLOEXEC-трубу (этим разблокируется fork/exec
		// родителя) в begin_new_exec, а mm->arg_start выставляет позже, и
		// всё это время /proc/<pid>/cmdline читается нулевой длины.
		// Опознавать этой функцией СВОЕГО только что порождённого ребёнка
		// поэтому нельзя — своего опознают по pid из Start, пока reaper его
		// не схоронил (см. procres.Runner.AlivePID, wdtt.process.pidIsOurs).
		return false
	}
	return base == filepath.Base(binary)
}

// MatchesAnyBinary reports whether pid's /proc cmdline argv0 basename equals
// one of basenames (сравнение тоже по basename — симметрично MatchesBinary,
// полный путь в basenames не молча проваливает сверку). Для случаев, когда
// известен только класс процесса (одна из нескольких известных программ), а
// не единственный ожидаемый путь — например, сирота-pidfile
// freeturn-*/wdtt-*, который может принадлежать либо клиенту, либо серверу.
// Как и MatchesBinary, fail-closed: недоступный или пустой cmdline — не
// совпадение.
func MatchesAnyBinary(pid int, basenames ...string) bool {
	base, ok := cmdlineArgv0Base(pid)
	if !ok {
		return false
	}
	for _, name := range basenames {
		if base == filepath.Base(name) {
			return true
		}
	}
	return false
}

// cmdlineArgv0Base reads pid's /proc cmdline and returns argv0's basename.
// ok is false if cmdline is unreadable or empty (zombie) — the fail-closed
// case shared by MatchesBinary and MatchesAnyBinary.
func cmdlineArgv0Base(pid int) (string, bool) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return "", false
	}
	argv0, _, _ := strings.Cut(string(b), "\x00")
	if argv0 == "" {
		return "", false
	}
	return filepath.Base(argv0), true
}

// StartTime — время старта процесса: поле 22 (starttime) из
// /proc/<pid>/stat, в тиках с момента загрузки. Вместе с номером процесса
// образует ОТПЕЧАТОК: номер сам по себе идентичностью не является — pid-файлы
// прокси лежат на флешке, переживают перезагрузку, и номер система
// переиспользует. Сверка по имени бинаря от этого не спасает, когда старое и
// новое поколение — один и тот же бинарь.
//
// ok=false — /proc/<pid>/stat нечитаем (процесса уже нет) или форма строки
// незнакома. Это не ошибка: добивать нечего.
func StartTime(pid int) (uint64, bool) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false
	}
	return parseStartTime(string(b))
}

// parseStartTime — разбор строки /proc/<pid>/stat от ПОСЛЕДНЕЙ закрывающей
// скобки: поле 2 — имя бинаря в скобках, а в нём бывают и пробелы, и сами
// скобки, поэтому разбиение всей строки по пробелам съезжает.
func parseStartTime(stat string) (uint64, bool) {
	end := strings.LastIndex(stat, ")")
	if end < 0 {
		return 0, false
	}
	// После имени идут поля 3 и далее: starttime — поле 22, то есть индекс 19.
	fields := strings.Fields(stat[end+1:])
	if len(fields) < 20 {
		return 0, false
	}
	v, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}
