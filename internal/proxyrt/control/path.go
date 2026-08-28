package control

import (
	"fmt"
	"path/filepath"
	"strings"
)

// maxSunPath — предел длины пути unix-сокета (sun_path), за вычетом
// завершающего нуля. Ядро длинный путь не обрезает, а отказывает (EINVAL), и
// отказ этот вылезает на процессе, а не на менеджере: проверяем при
// формировании.
const maxSunPath = 107

// maxInstanceLen — идентификатор инстанса входит и в имя сокета, и в имя
// журнала.
const maxInstanceLen = 32

// ValidateInstance проверяет идентификатор инстанса: [A-Za-z0-9_-], до 32.
func ValidateInstance(id string) error {
	if id == "" {
		return fmt.Errorf("пустой идентификатор инстанса")
	}
	if len(id) > maxInstanceLen {
		return fmt.Errorf("идентификатор инстанса %q длиннее %d символов", id, maxInstanceLen)
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return fmt.Errorf("идентификатор инстанса %q содержит недопустимый символ %q", id, r)
		}
	}
	return nil
}

// SocketPath собирает путь управляющего сокета: <dir>/<impl>-<role>-<id>.sock.
//
// Имя включает impl и роль, потому что «default» — идентификатор по умолчанию
// сразу в двух подсистемах, а ролей четыре: без этого четыре процесса пришли бы
// на один путь.
func SocketPath(dir, impl, role, instance string) (string, error) {
	return buildPath(dir, impl, role, instance, ".sock")
}

// LogPath собирает путь журнала процесса по тому же правилу.
func LogPath(dir, impl, role, instance string) (string, error) {
	return buildPath(dir, impl, role, instance, ".log")
}

func buildPath(dir, impl, role, instance, ext string) (string, error) {
	if err := ValidateInstance(instance); err != nil {
		return "", err
	}
	if impl == "" || role == "" {
		return "", fmt.Errorf("impl и роль обязательны в имени файла инстанса")
	}
	path := filepath.Join(dir, strings.Join([]string{impl, role, instance}, "-")+ext)
	if ext == ".sock" && len(path) > maxSunPath {
		return "", fmt.Errorf("путь сокета длиной %d байт превышает предел sun_path (%d): %s",
			len(path), maxSunPath, path)
	}
	return path, nil
}
