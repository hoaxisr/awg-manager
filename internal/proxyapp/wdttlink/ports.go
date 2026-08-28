package wdttlink

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
)

// Режимы связи клиента и сервера — те же литералы, что в конфигах ролей
// (roles.WdttClientConfig.Mode, roles.WdttServerConfig.RelayMode).
const (
	ConnModeWG  = "wg"
	ConnModeRaw = "raw"
)

const (
	// defaultServerWgPort / defaultClientListenPort — дефолты старого мира
	// (wdtt.DefaultServerConfig().WgPort и порт из DefaultClientConfig().Listen).
	// Здесь константами: конфигов-дефолтов у нового мира нет, а ссылка обязана
	// собираться и при пустых полях записи.
	defaultServerWgPort     = 56001
	defaultClientListenPort = 9000
)

// normalizeConnMode returns wg or raw (default wg).
func normalizeConnMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case ConnModeRaw:
		return ConnModeRaw
	default:
		return ConnModeWG
	}
}

func listenPort(addr string) (int, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return 0, fmt.Errorf("адрес прослушивания не задан")
	}
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, fmt.Errorf("некорректный адрес %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return 0, fmt.Errorf("некорректный порт в %q", addr)
	}
	return port, nil
}

// LinkListenPort — WAN-порт, который абонент кладёт в peer: режим берётся из
// самой записи (RelayMode).
func LinkListenPort(c roles.WdttServerConfig) int {
	return LinkListenPortForMode(c, c.RelayMode)
}

// LinkListenPortForMode — тот же порт для ЯВНО заданного режима (§11: ссылку
// на raw-порт можно выдать и с wg-сервера, и наоборот).
//
// mode: raw → -listen-raw (явный либо DTLS+1); wg → -listen-direct, если задан,
// иначе -listen (DTLS). Фолбэки 56003/56002 — прежние (ports.go:50-63).
// EffectiveRawListen не копируется: он уже живёт на конфиге роли
// (roles.WdttServerConfig.EffectiveRawListen, config.go) — второй копии
// правила «пусто = DTLS+1» здесь быть не должно.
func LinkListenPortForMode(c roles.WdttServerConfig, mode string) int {
	if normalizeConnMode(mode) == ConnModeRaw {
		if p, err := listenPort(c.EffectiveRawListen()); err == nil {
			return p
		}
		return 56003
	}
	addr := c.Listen
	if direct := strings.TrimSpace(c.DirectListen); direct != "" {
		addr = direct
	}
	if p, err := listenPort(addr); err == nil {
		return p
	}
	return 56002
}
