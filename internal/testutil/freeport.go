//go:build linux

package testutil

import (
	"fmt"
	"math/rand/v2"
	"net"
	"sync"
	"syscall"
	"testing"
)

// Диапазон ниже ip_local_port_range (32768+): ядро не отдаёт эти номера ни под
// «:0», ни под исходящее соединение.
const (
	freePortLow  = 15000
	freePortHigh = 32000
)

var (
	freePortMu    sync.Mutex
	freePortNext  = freePortLow + rand.IntN(freePortHigh-freePortLow)
	freePortLocks []net.Listener // держатся до выхода процесса
)

// FreePort — порт на 127.0.0.1, который тест займёт сам или отдаст ребёнку.
//
// Приём «Listen(:0) → Close → занять тот же номер» — гонка: номер лежит в
// эфемерном диапазоне, и между Close и занятием его получает чужой слушатель
// или исходящее соединение любого из параллельных тестовых процессов. Здесь
// номер вне эфемерного диапазона и закреплён за процессом абстрактным
// unix-сокетом: ядро снимает его само с выходом процесса.
func FreePort(t testing.TB) int {
	t.Helper()
	freePortMu.Lock()
	defer freePortMu.Unlock()
	for range freePortHigh - freePortLow {
		port := freePortNext
		if freePortNext++; freePortNext == freePortHigh {
			freePortNext = freePortLow
		}
		lock, err := net.Listen("unix", fmt.Sprintf("@awgm-test-port-%d", port))
		if err != nil {
			continue
		}
		if !portBindable(port) {
			_ = lock.Close()
			continue
		}
		freePortLocks = append(freePortLocks, lock)
		return port
	}
	t.Fatal("нет свободных портов для теста")
	return 0
}

// FreeAddr — FreePort в виде 127.0.0.1:порт.
func FreeAddr(t testing.TB) string {
	t.Helper()
	return fmt.Sprintf("127.0.0.1:%d", FreePort(t))
}

// portBindable занимает порт, но не слушает: копия слушающего сокета, унаследованная
// ребёнком в окне fork→exec, держала бы порт занятым и после Close, а
// неслушающему с SO_REUSEADDR следующий bind не мешает.
func portBindable(port int) bool {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return false
	}
	defer syscall.Close(fd)
	if syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1) != nil {
		return false
	}
	return syscall.Bind(fd, &syscall.SockaddrInet4{Port: port}) == nil
}
