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

// NDMSNamed — конфиг роли, объявляющий свои NDMS-имена. Его реализуют все
// четыре конфига (roles/config.go), в том числе те, у кого имён нет.
//
// Интерфейс, а не []any с разбором типов: разбор МОЛЧА пропускал незнакомое —
// указатель на конфиг вместо значения, переименованный или новый тип. Имена
// живого интерфейса не попадали в ведомость, и уборщик сносил РАБОЧИЙ
// интерфейс без следа в журнале. Теперь незнакомое не компилируется:
//
//	type новыйКонфиг struct{}
//	DeclaredNDMSNames([]NDMSNamed{новыйКонфиг{}})
//	// cannot use новыйКонфиг{} … as NDMSNamed value … missing method NDMSNames
//
// Методы объявлены на ЗНАЧЕНИИ, поэтому указатель на конфиг интерфейсу тоже
// удовлетворяет и даёт те же имена (метод-сет *T включает методы T).
type NDMSNamed interface {
	NDMSNames() []string
}

// DeclaredNDMSNames — ведомость NDMS-имён, объявленных инстансами. Включает
// ВЫКЛЮЧЕННЫЕ инстансы: sweep сносит только ресурсы без живой декларации
// (спека §4.2), а disabled — живая декларация.
func DeclaredNDMSNames(cfgs []NDMSNamed) map[string]bool {
	out := map[string]bool{}
	for _, cfg := range cfgs {
		for _, name := range cfg.NDMSNames() {
			if name != "" {
				out[name] = true
			}
		}
	}
	return out
}
