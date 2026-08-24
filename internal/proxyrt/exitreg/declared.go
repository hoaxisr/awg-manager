package exitreg

import (
	"strings"

	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/roles/wdttclient"
)

// InstanceConfig — пара «идентификатор инстанса + его конфиг». Идентификатора
// в самом конфиге нет: роль получает его отдельно (roles/wdttclient/role.go:49).
//
// Cfg типизирован ИНТЕРФЕЙСОМ, а не any: конфиг без метода RawExit не
// соберётся вовсе — вместо того чтобы молча выпасть из ведомости и отдать
// свою зеркальную запись (с настройками пользователя, которых в конфиге нет)
// уборке. Та же форма и по той же причине, что instance.NDMSNamed.
type InstanceConfig struct {
	ID      string
	Cfg     roles.RawExiter
	Enabled bool // намерение инстанса; писатель конфига — план 5
}

// DeclaredExits — ведомость выходов. ПОЛНЫЙ список на вход, полный на выход:
// то, чего здесь нет, реестр считает снятым и удаляет зеркальную запись.
// Включает ВЫКЛЮЧЕННЫЕ инстансы — disabled это живое объявление
// (§4.2), и выход выключенного инстанса обязан разрешаться в имя.
func DeclaredExits(list []InstanceConfig) []ExitDecl {
	out := make([]ExitDecl, 0, len(list))
	for _, ic := range list {
		if ic.Cfg == nil {
			// Дефект писателя. Уронить из-за него ВЕСЬ список нельзя:
			// у остальных инстансов зеркальные записи живые.
			//
			// ГРАНИЦА ГАРДА, названная честно (M3): он ловит НЕТИПИЗИРОВАННЫЙ
			// nil — незаполненное поле в литерале. Типизированный
			// (*roles.WdttClientConfig)(nil) в интерфейсном поле гард пройдёт
			// и уронит панику на RawExit(): все четыре метода объявлены на
			// ЗНАЧЕНИИ (roles/config.go, задача 1), и вызов через nil-указатель
			// разыменовывает его. Рефлексию сюда не заводим — см. обоснование
			// в брифе задачи; требование к писателю — план 5, п. 19.
			continue
		}
		e, ok := ic.Cfg.RawExit()
		if !ok {
			continue
		}
		out = append(out, ExitDecl{
			ID:          wdttclient.RawTunnelID(ic.ID),
			InstanceID:  ic.ID,
			Name:        e.Name,
			NDMSName:    e.NDMSName,
			KernelIface: e.KernelIface,
			Peer:        e.Peer,
			Enabled:     ic.Enabled,
		})
	}
	return out
}

// MirrorName — имя зеркальной записи. Паритет с wdtt.TunnelNameFromClient
// (names.go:5-21): суффикс, дефолт и усечение обязаны совпадать, иначе после
// апгрейда у пользователя переименуются все карточки разом.
func MirrorName(name string) string {
	n := strings.TrimSpace(name)
	if n == "" {
		n = "WDTT"
	}
	const suffix = " wdtt"
	if !strings.HasSuffix(strings.ToLower(n), suffix) {
		n += suffix
	}
	// По рунам, а не по байтам: срез на 60 байтах рвёт кириллицу пополам.
	if r := []rune(n); len(r) > 60 {
		n = string(r[:60])
	}
	return n
}
