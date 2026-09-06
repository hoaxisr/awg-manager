package obfuscator

import (
	"os"
	"path/filepath"
	"strings"
)

// Пути установщика Phobos на Keenetic (router-configure-wireguard.sh,
// install-obfuscator.sh). Var — тесты подменяют.
var (
	foreignInitScript = "/opt/etc/init.d/S49wg-obfuscator"
	procRoot          = "/proc"
)

// Foreign — следы родной установки Phobos. Не перенимаем (Q5): только
// предупреждаем и показываем как «внешний».
type Foreign struct {
	InitScript   bool // /opt/etc/init.d/S49wg-obfuscator
	ProcessAlive bool // процесс с argv0 wg-obfuscator* (их имя, не наше awgm-…)
}

func DetectForeign() Foreign {
	var f Foreign
	if _, err := os.Stat(foreignInitScript); err == nil {
		f.InitScript = true
	}
	entries, _ := os.ReadDir(procRoot)
	for _, e := range entries {
		if !e.IsDir() || e.Name()[0] < '0' || e.Name()[0] > '9' {
			continue
		}
		b, err := os.ReadFile(filepath.Join(procRoot, e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		argv0, _, _ := strings.Cut(string(b), "\x00")
		if strings.HasPrefix(filepath.Base(argv0), "wg-obfuscator") {
			f.ProcessAlive = true
			break
		}
	}
	return f
}

func (f Foreign) Warning() string {
	switch {
	case f.InitScript && f.ProcessAlive:
		return "На роутере найдена установка Phobos (S49wg-obfuscator, процесс запущен). Она не перенимается и продолжит работать отдельно; порты не пересекаются."
	case f.InitScript:
		return "На роутере найден init-скрипт установки Phobos (S49wg-obfuscator). Он не перенимается."
	case f.ProcessAlive:
		return "На роутере запущен чужой wg-obfuscator. Он не перенимается."
	}
	return ""
}

// ExternalKind — метка «внешний» для системного туннеля по description,
// который ставит установщик Phobos: Phobos-<client>.
func ExternalKind(description string) string {
	if strings.HasPrefix(description, "Phobos-") {
		return "phobos"
	}
	return ""
}
