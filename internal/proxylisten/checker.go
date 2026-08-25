package proxylisten

import (
	"net"
	"strconv"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/proxyrt/instancestore"
	"github.com/hoaxisr/awg-manager/internal/storage"
)

// RecordLister — срез хранилища прокси-инстансов: одно чтение записей обеих
// подсистем. Объявлен здесь, у потребителя: пакету нужен ровно этот метод, а
// не всё хранилище.
type RecordLister interface {
	Load() (instancestore.State, error)
}

// CrossChecker reports localhost UDP ports used by AWG tunnel peer endpoints
// and by proxy instances of both subsystems (FreeTurn ↔ WDTT), plus WAN/server
// ports (FreeTurn/WDTT DTLS listen and WDTT internal WG).
type CrossChecker struct {
	AWGStore *storage.AWGTunnelStore

	Records RecordLister
}

func anyHostPort(addr string) (int, bool) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return 0, false
	}
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return 0, false
	}
	return port, true
}

// localHostPort — порт локального адреса: клиент обеих подсистем слушает
// только 127.0.0.1, и адрес другого хоста локальный порт не занимает.
func localHostPort(addr string) (int, bool) {
	host, portStr, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return 0, false
	}
	switch strings.Trim(strings.ToLower(host), "[]") {
	case "127.0.0.1", "localhost", "::1":
	default:
		return 0, false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return 0, false
	}
	return port, true
}

// OccupiedLocalListenPorts собирает занятые порты. excludeWdttClientID и
// excludeFreeTurnClientID — клиент, для которого порт и подбирается: его
// собственный AWG-туннель указывает на 127.0.0.1:<его listen>, и без этого
// исключения клиент считал бы свой же порт занятым и переезжал на соседний,
// оставляя endpoint туннеля висеть на старом (endpoint нигде не обновляется).
// По той же причине исключается и СОБСТВЕННАЯ запись клиента: записи обеих
// подсистем лежат в одном хранилище, и без исключения клиент нашёл бы там
// свой же порт.
func (c *CrossChecker) OccupiedLocalListenPorts(excludeWdttClientID, excludeFreeTurnClientID string) (map[int]bool, error) {
	used := map[int]bool{}

	if c.AWGStore != nil {
		tunnels, err := c.AWGStore.List()
		if err != nil {
			return nil, err
		}
		for _, tun := range tunnels {
			if excludeWdttClientID != "" && strings.TrimSpace(tun.WdttClientID) == excludeWdttClientID {
				continue
			}
			if excludeFreeTurnClientID != "" && strings.TrimSpace(tun.FreeTurnClientID) == excludeFreeTurnClientID {
				continue
			}
			if port, ok := localHostPort(tun.Peer.Endpoint); ok {
				used[port] = true
			}
		}
	}

	if c.Records != nil {
		st, err := c.Records.Load()
		if err != nil {
			return nil, err
		}
		for _, rec := range st.Records {
			switch {
			case rec.WdttClient != nil:
				if rec.ID == excludeWdttClientID {
					continue
				}
				if port, ok := localHostPort(rec.WdttClient.Listen); ok {
					used[port] = true
				}
			case rec.FreeTurnClient != nil:
				if rec.ID == excludeFreeTurnClientID {
					continue
				}
				if port, ok := localHostPort(rec.FreeTurnClient.Listen); ok {
					used[port] = true
				}
			case rec.WdttServer != nil:
				if port, ok := anyHostPort(rec.WdttServer.Listen); ok {
					used[port] = true
				}
				if rec.WdttServer.WgPort > 0 {
					used[rec.WdttServer.WgPort] = true
				}
			case rec.FreeTurnServer != nil:
				if port, ok := anyHostPort(rec.FreeTurnServer.Listen); ok {
					used[port] = true
				}
			}
		}
	}

	return used, nil
}
