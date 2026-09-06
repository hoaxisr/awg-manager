package obfuscator

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// RenderConf — конфиг релея (формат wg-obfuscator.conf, одна секция).
// verbose наш: INFO даёт строки старта/подключения в app-журнал (Q18).
func RenderConf(o *storage.Obfuscator) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[main]\nsource-if = 127.0.0.1\nsource-lport = %d\ntarget = %s\nkey = %s\nmasking = %s\nmax-dummy = %d\n",
		o.LocalPort, o.Target, o.Key, o.Masking, o.MaxDummy)
	if o.IdleTimeout > 0 {
		fmt.Fprintf(&b, "idle-timeout = %d\n", o.IdleTimeout)
	}
	if o.Flavor == storage.ObfuscatorFlavorPhobos && o.ObfuscateBytes > 0 {
		fmt.Fprintf(&b, "obfuscate-bytes = %d\n", o.ObfuscateBytes)
	}
	b.WriteString("verbose = INFO\n")
	return b.String()
}

func ConfPath(tunnelID string) string { return filepath.Join(ConfDir, tunnelID+".conf") }

// WriteConf пишет конфиг с ключом: 0600, каталог 0700.
func WriteConf(tunnelID string, o *storage.Obfuscator) error {
	if err := os.MkdirAll(ConfDir, 0o700); err != nil {
		return fmt.Errorf("obfuscator conf dir: %w", err)
	}
	return os.WriteFile(ConfPath(tunnelID), []byte(RenderConf(o)), 0o600)
}

// RemoveConf — отсутствие файла не ошибка.
func RemoveConf(tunnelID string) { _ = os.Remove(ConfPath(tunnelID)) }

// PortFree — UDP-порт на 127.0.0.1 свободен (bind-проба).
func PortFree(port int) bool {
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// PickLocalPort выбирает первый порт пула, не занятый ни другим туннелем
// (taken — по стору), ни чужим процессом (bind-проба). Пул наш: 13255
// установщика Phobos и 9000+ wdtt сюда не попадают (Q6).
func PickLocalPort(taken func(port int) bool) (int, error) {
	for p := PortMin; p <= PortMax; p++ {
		if taken(p) || !PortFree(p) {
			continue
		}
		return p, nil
	}
	return 0, errors.New("нет свободного loopback-порта для обфускатора (39000–39099)")
}
