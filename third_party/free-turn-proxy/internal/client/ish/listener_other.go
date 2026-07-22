//go:build !(linux && 386)

// Package ish - TCP listener shim для iSH (https://ish.app), Linux user-mode
// эмулятор на iOS. iSH таргетит linux/386 и не имеет современного accept4 /
// epoll-poller Go, поэтому там нужен sandbox-aware accept loop. На любом
// другом GOOS/GOARCH WrapListener - no-op passthrough.
package ish

import "net"

// WrapListener возвращает ln без изменений на не-iSH платформах.
func WrapListener(ln net.Listener) (net.Listener, error) {
	return ln, nil
}
