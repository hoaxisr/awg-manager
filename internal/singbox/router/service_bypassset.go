package router

import (
	"fmt"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// GeoIPTagCounter — узкий контракт бюджет-валидации geoip-bypass.
// *hydraroute.GeoDataStore удовлетворяет (GeoIPTagCounts).
type GeoIPTagCounter interface {
	GeoIPTagCounts() map[string]int
}

const bypassSetMaxElem = 262144 // = maxelem набора AWGM-BYPASS

// validateBypassGeoIPTags: суммарный размер выбранных geoip-тегов не
// превышает maxelem. Проверка КОНСЕРВАТИВНА: Count учитывает и IPv6-элементы
// .dat, в набор кладётся только IPv4 — ложный отказ возможен лишь вплотную
// к пределу.
func (s *ServiceImpl) validateBypassGeoIPTags(sr storage.SingboxRouterSettings) error {
	if len(sr.BypassGeoIPTags) == 0 || s.deps.GeoTagCounts == nil {
		return nil
	}
	total := 0
	counts := s.deps.GeoTagCounts.GeoIPTagCounts()
	for _, tag := range sr.BypassGeoIPTags {
		total += counts[strings.ToLower(strings.TrimSpace(tag))]
	}
	if total > bypassSetMaxElem {
		return fmt.Errorf("geoip-обход: выбрано ~%d записей при пределе %d — уберите часть тегов", total, bypassSetMaxElem)
	}
	return nil
}
