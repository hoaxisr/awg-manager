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
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		// Личность не подтверждается: pid исчез между kill(0) и чтением либо
		// cmdline недоступен. Fail-closed — считаем, что это не наш процесс.
		return false
	}
	argv0, _, _ := strings.Cut(string(b), "\x00")
	if argv0 == "" {
		return false // зомби: cmdline пуст
	}
	return filepath.Base(argv0) == filepath.Base(binary)
}

// MatchesAnyBinary reports whether pid's /proc cmdline argv0 basename equals
// one of basenames. Для случаев, когда известен только класс процесса (одна
// из нескольких известных программ), а не единственный ожидаемый путь —
// например, сирота-pidfile freeturn-*/wdtt-*, который может принадлежать
// либо клиенту, либо серверу. Как и MatchesBinary, fail-closed: недоступный
// или пустой cmdline — не совпадение.
func MatchesAnyBinary(pid int, basenames ...string) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return false
	}
	argv0, _, _ := strings.Cut(string(b), "\x00")
	if argv0 == "" {
		return false
	}
	base := filepath.Base(argv0)
	for _, name := range basenames {
		if base == name {
			return true
		}
	}
	return false
}
