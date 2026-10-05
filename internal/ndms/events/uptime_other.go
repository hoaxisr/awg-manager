//go:build !linux

package events

// ReadUptime — заглушка вне linux: аптайм неизвестен, «hook age» не пишется.
func ReadUptime() float64 { return 0 }
