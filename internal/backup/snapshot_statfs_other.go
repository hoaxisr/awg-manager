//go:build !linux

package backup

func availableBytes(string) (int64, bool) { return 0, false }
