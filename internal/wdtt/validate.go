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

// ensureUniqueListenAddr returns addr when its port is free among listens[selfIdx]
// and reserved (AWG tunnel endpoints, sibling proxy clients); otherwise the next
// free loopback port in [portMin, portMax).
func ensureUniqueListenAddr(listens []string, selfIdx int, addr string, reserved map[int]bool, portMin, portMax int) string {
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
	if selfPort, err := listenPort(addr); err == nil && !used[selfPort] {
		return addr
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

func findServerIndex(servers []ServerInstance, id string) int {
	for i, s := range servers {
		if s.ID == id {
			return i
		}
	}
	return -1
}

func ensureUniqueServerListenAddr(listens []string, selfIdx int, addr string, reserved map[int]bool, portMin, portMax int) string {
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
	if selfPort, err := listenPort(addr); err == nil && !used[selfPort] {
		return addr
	}
	host := "0.0.0.0"
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

func ensureUniqueWgPort(wgPort int, listenAddr string, reserved map[int]bool, portMin, portMax int) int {
	if wgPort <= 0 {
		wgPort = DefaultServerConfig().WgPort
	}
	used := map[int]bool{}
	for port, v := range reserved {
		if v {
			used[port] = true
		}
	}
	if lp, err := listenPort(listenAddr); err == nil {
		used[lp] = true
	}
	if !used[wgPort] {
		return wgPort
	}
	for port := portMin; port < portMax; port++ {
		if !used[port] {
			return port
		}
	}
	return wgPort
}

func serverListenAddresses(servers []ServerInstance) []string {
	out := make([]string, len(servers))
	for i, s := range servers {
		out[i] = s.Config.Listen
	}
	return out
}

func nextServerListen(servers []ServerInstance, reserved map[int]bool) string {
	used := map[int]bool{}
	for _, s := range servers {
		if port, err := listenPort(s.Config.Listen); err == nil {
			used[port] = true
		}
		if s.Config.WgPort > 0 {
			used[s.Config.WgPort] = true
		}
	}
	for port, v := range reserved {
		if v {
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
	OccupiedLocalListenPorts(excludeWdttClientID, excludeFreeTurnClientID string) (map[int]bool, error)
}
