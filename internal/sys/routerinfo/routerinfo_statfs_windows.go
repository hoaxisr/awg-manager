//go:build windows

package routerinfo

func statfsUsage(path string) (used, total int64, ok bool) {
	return 0, 0, false
}

func IsUBIFS(path string) bool {
	return false
}
