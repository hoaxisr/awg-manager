package childproc

import (
	"fmt"
	"os"
	"path/filepath"
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
