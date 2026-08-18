package instance

import (
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
)

// SweepLabels — метки владения NDMS для sweeper'а (движок, план 1).
//
// ТРЕБОВАНИЕ К СКАНЕРУ (прод-адаптер — план 5): совпадение по ПРЕФИКСУ
// description, не по равенству. Старый скан сравнивал равенством
// (router_adapters.go:339) и потому НИКОГДА не находил клиентские сироты —
// их description это имя инстанса, а не константа (ndms_iface.go:452 против
// client_ndms.go:123). Метка-префикс LabelClientPrefix чинит класс.
func SweepLabels() []string {
	return []string{roles.LabelServerWG, roles.LabelServerRaw, roles.LabelClientPrefix}
}

// DeclaredNDMSNames — ведомость NDMS-имён, объявленных инстансами. Включает
// ВЫКЛЮЧЕННЫЕ инстансы: sweep сносит только ресурсы без живой декларации
// (спека §4.2), а disabled — живая декларация.
func DeclaredNDMSNames(cfgs []any) map[string]bool {
	out := map[string]bool{}
	add := func(name string) {
		if name != "" {
			out[name] = true
		}
	}
	for _, cfg := range cfgs {
		switch c := cfg.(type) {
		case roles.WdttServerConfig:
			add(c.NdmsIface)
			add(c.RawNdmsIface)
		case roles.WdttClientConfig:
			add(c.NdmsIface)
		}
	}
	return out
}
