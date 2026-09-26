package obfuscator

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// ErrKernelIPv4Only — модуль awgm_relay только IPv4 (спека §2); диспетчер
// отдаёт такой туннель процессу.
var ErrKernelIPv4Only = errors.New("awgm_relay: только IPv4-адрес сервера")

// EffectiveKey — ключ так, как его видит INI-разбор Phobos/ClusterM
// (config.c: '#' — комментарий до конца строки, значение — до следующего '=',
// пробелы по краям срезаются). Процесс получает сырой ключ и режет его сам;
// модулю отдаём уже эффективный, иначе совместимости с сервером нет (F470).
func EffectiveKey(raw string) string {
	if i := strings.IndexByte(raw, '#'); i >= 0 {
		raw = raw[:i]
	}
	if i := strings.IndexByte(raw, '='); i >= 0 {
		raw = raw[:i]
	}
	return strings.Trim(raw, " \t\r\n")
}

// EffectiveObfuscateBytes — Phobos при MEDIA и незаданном obfuscate-bytes
// берёт 16 (config.c, MEDIA_OBFUSCATE_BYTES_DEFAULT); без этого модуль
// XOR-ит весь пакет, а сервер — 16 байт, и рукопожатие молча не проходит.
func EffectiveObfuscateBytes(o *storage.Obfuscator) int {
	if o.Masking == "MEDIA" && o.ObfuscateBytes == 0 {
		return 16
	}
	return o.ObfuscateBytes
}

// KernelMasking — AUTO у клиента на проводе ≡ NONE (обработчик маскировки
// не ставится, автоопределение делает сервер).
func KernelMasking(o *storage.Obfuscator) string {
	switch o.Masking {
	case "STUN":
		return "stun"
	case "MEDIA":
		return "media"
	}
	return "none"
}

// AddLine — строка /proc/awgm_relay/add (спека §3.3). Содержит ключ —
// не логировать.
func AddLine(o *storage.Obfuscator, ip string) (string, error) {
	if p := net.ParseIP(ip); p == nil || p.To4() == nil {
		return "", ErrKernelIPv4Only
	}
	_, port, err := net.SplitHostPort(o.Target)
	if err != nil {
		return "", fmt.Errorf("target %q: %w", o.Target, err)
	}
	return fmt.Sprintf("127.0.0.1:%d %s:%s transform=phobos key=%s masking=%s max-dummy=%d obfuscate-bytes=%d",
		o.LocalPort, ip, port, hex.EncodeToString([]byte(EffectiveKey(o.Key))),
		KernelMasking(o), o.MaxDummy, EffectiveObfuscateBytes(o)), nil
}
