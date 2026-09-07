// Package obfuscator — userspace-релей wg-obfuscator (Phobos / ClusterM):
// формат [instance] и phobos://, конфиг релея, процесс, детект чужой установки,
// загрузка пакета по install-ссылке. Спека: docs/2026-09-06-wg-obfuscator-spec.md.
package obfuscator

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
	"github.com/hoaxisr/awg-manager/internal/tunnel/config"
)

const (
	// ConfDir — конфиги релея (в них ключ: 0600). Var — тесты подменяют.
	// RunDir — pidfile и stderr-лог, tmpfs (#854: ничего повторяющегося на флеш).
	PortMin = 39000
	PortMax = 39099

	// DetailsNotRunning — StateInfo.Details, когда WG-интерфейс стоит, а релея нет.
	DetailsNotRunning = "обфускатор не запущен"
)

var (
	ConfDir = "/opt/etc/awg-manager/obfuscator"
	RunDir  = "/var/run/awg-manager/obfuscator"
	BinDir  = "/opt/bin"
)

// BinaryName — имя бинаря разновидности. Не пересекается с /opt/bin/wg-obfuscator
// установщика Phobos (Q18).
func BinaryName(flavor string) string { return "awgm-wg-obfuscator-" + flavor }

var maskings = map[string]map[string]bool{
	storage.ObfuscatorFlavorPhobos:   {"STUN": true, "MEDIA": true, "AUTO": true, "NONE": true},
	storage.ObfuscatorFlavorClusterM: {"STUN": true, "AUTO": true, "NONE": true},
}

// Validate проверяет пользовательские поля. nil — обычный туннель, валиден.
func Validate(o *storage.Obfuscator) error {
	if o == nil {
		return nil
	}
	allowed, ok := maskings[o.Flavor]
	if !ok {
		return fmt.Errorf("неизвестная разновидность обфускатора %q", o.Flavor)
	}
	// Key/Target уезжают в INI релея как есть (RenderConf/RenderInstance):
	// перевод строки или `[` подменили бы соседние ключи и секции. Через
	// [instance] такое не приходит (парсер режет по строкам), а через JSON
	// API/MCP — приходит.
	if strings.ContainsAny(o.Key, "\r\n[") || strings.ContainsAny(o.Target, "\r\n[") {
		return errors.New("недопустимые символы в key или target")
	}
	target, err := config.NormalizeEndpoint(o.Target)
	if err != nil {
		return fmt.Errorf("target: %w", err)
	}
	if host, _, _ := net.SplitHostPort(target); host == "" || host == "localhost" ||
		(net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()) {
		return errors.New("target должен указывать на сервер, а не на loopback")
	}
	if strings.TrimSpace(o.Key) == "" {
		return errors.New("key обязателен")
	}
	if !allowed[o.Masking] {
		return fmt.Errorf("masking %q недоступен для %s", o.Masking, o.Flavor)
	}
	if o.MaxDummy < 0 || o.MaxDummy > 1024 {
		return errors.New("max-dummy: 0..1024")
	}
	if o.IdleTimeout < 0 {
		return errors.New("idle-timeout не может быть отрицательным")
	}
	if o.ObfuscateBytes < 0 {
		return errors.New("obfuscate-bytes не может быть отрицательным")
	}
	if o.ObfuscateBytes != 0 && o.Flavor != storage.ObfuscatorFlavorPhobos {
		return errors.New("obfuscate-bytes есть только у phobos")
	}
	return nil
}

// Equal — сравнение по значению с учётом nil.
func Equal(a, b *storage.Obfuscator) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
