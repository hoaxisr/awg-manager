package freeturn

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

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

func validateUniqueListens(listens []string, selfIdx int, selfAddr string) error {
	selfPort, err := listenPort(selfAddr)
	if err != nil {
		return err
	}
	for i, addr := range listens {
		if i == selfIdx {
			continue
		}
		port, err := listenPort(addr)
		if err != nil {
			continue
		}
		if port == selfPort {
			return fmt.Errorf("порт %d уже занят другим экземпляром", port)
		}
	}
	return nil
}

func clientListenAddresses(clients []ClientInstance) []string {
	out := make([]string, len(clients))
	for i, c := range clients {
		out[i] = c.Config.Listen
	}
	return out
}

func serverListenAddresses(servers []ServerInstance) []string {
	out := make([]string, len(servers))
	for i, s := range servers {
		out[i] = s.Config.Listen
	}
	return out
}

func nextClientListen(clients []ClientInstance) string {
	used := map[int]bool{}
	for _, c := range clients {
		if port, err := listenPort(c.Config.Listen); err == nil {
			used[port] = true
		}
	}
	for port := 9000; port < 9100; port++ {
		if !used[port] {
			return fmt.Sprintf("127.0.0.1:%d", port)
		}
	}
	return "127.0.0.1:9000"
}

func nextServerListen(servers []ServerInstance) string {
	used := map[int]bool{}
	for _, s := range servers {
		if port, err := listenPort(s.Config.Listen); err == nil {
			used[port] = true
		}
	}
	for port := 56000; port < 56100; port++ {
		if !used[port] {
			return fmt.Sprintf("0.0.0.0:%d", port)
		}
	}
	return "0.0.0.0:56000"
}
