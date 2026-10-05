//go:build linux

package events

import (
	"os"
	"strconv"
	"strings"
)

// ReadUptime — аптайм системы в секундах из /proc/uptime; 0 при отказе.
// Пара к полю t= хук-скрипта (Event.ScriptUptime) — только для журнала.
func ReadUptime() float64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	f, _, _ := strings.Cut(string(data), " ")
	up, err := strconv.ParseFloat(f, 64)
	if err != nil {
		return 0
	}
	return up
}
