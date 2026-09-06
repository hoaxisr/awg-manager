package router

import "strings"

// StoreDNSForCachePath — писать ли DNS-кэш в cache.db (store_dns, sing-box
// 1.14). Только для кэша в RAM: путь под /tmp (tmpfs на роутере). На флеше
// это износ ради нескольких секунд после перезапуска. Одно правило для базы
// (00-base.json) и fakeip-слота, иначе последний-победивший experimental
// расходился бы с базой.
func StoreDNSForCachePath(path string) bool {
	return strings.HasPrefix(path, "/tmp/")
}
