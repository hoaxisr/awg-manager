package routerinfo

import "syscall"

func statfsUsage(path string) (used, total int64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil || st.Blocks <= 0 {
		return 0, 0, false
	}

	blockSize := int64(st.Bsize)
	total = int64(st.Blocks) * blockSize
	used = int64(st.Blocks-st.Bfree) * blockSize
	if total <= 0 || used < 0 {
		return 0, 0, false
	}
	return used, total, true
}

// ubifsMagic — UBIFS_SUPER_MAGIC из linux/magic.h.
const ubifsMagic = 0x24051905

// IsUBIFS сообщает, лежит ли path на ubifs (внутренняя NAND-память Keenetic).
// Тип Statfs_t.Type зависит от арки, отсюда uint32.
func IsUBIFS(path string) bool {
	var st syscall.Statfs_t
	return syscall.Statfs(path, &st) == nil && uint32(st.Type) == ubifsMagic
}
