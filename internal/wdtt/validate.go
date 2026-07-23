package wdtt

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

// ensureUniqueListenAddr returns addr unchanged if unique among listens[selfIdx],
// otherwise the next free loopback port in [portMin, portMax).
func ensureUniqueListenAddr(listens []string, selfIdx int, addr string, reserved map[int]bool, portMin, portMax int) string {
	if err := validateUniqueListens(listens, selfIdx, addr); err == nil {
		return addr
	}
	used := map[int]bool{}
	for i, a := range listens {
		if i == selfIdx {
			continue
		}
		if port, err := listenPort(a); err == nil {
			used[port] = true
		}
	}
	for port, v := range reserved {
		if v {
			used[port] = true
		}
	}
	host := "127.0.0.1"
	if h, _, err := net.SplitHostPort(addr); err == nil && strings.TrimSpace(h) != "" {
		host = h
	}
	for port := portMin; port < portMax; port++ {
		if !used[port] {
			return fmt.Sprintf("%s:%d", host, port)
		}
	}
	return fmt.Sprintf("%s:%d", host, portMin)
}

func clientListenAddresses(clients []ClientInstance) []string {
	out := make([]string, len(clients))
	for i, c := range clients {
		out[i] = c.Config.Listen
	}
	return out
}

func nextClientListen(clients []ClientInstance, reserved map[int]bool) string {
	used := map[int]bool{}
	for _, c := range clients {
		if port, err := listenPort(c.Config.Listen); err == nil {
			used[port] = true
		}
	}
	for port, v := range reserved {
		if v {
			used[port] = true
		}
	}
	for port := 9000; port < 9200; port++ {
		if !used[port] {
			return fmt.Sprintf("127.0.0.1:%d", port)
		}
	}
	return "127.0.0.1:9100"
}

func findClientIndex(clients []ClientInstance, id string) int {
	for i, c := range clients {
		if c.ID == id {
			return i
		}
	}
	return -1
}

// LocalListenPort parses host:port and returns port when host is loopback.
func LocalListenPort(addr string) (int, bool) {
	addr = strings.TrimSpace(addr)
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, false
	}
	host = strings.Trim(strings.ToLower(host), "[]")
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return 0, false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 {
		return 0, false
	}
	return port, true
}

type LocalListenPortChecker interface {
	OccupiedLocalListenPorts() (map[int]bool, error)
}
