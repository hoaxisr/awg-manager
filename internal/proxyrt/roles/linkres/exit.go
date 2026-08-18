package linkres

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/hoaxisr/awg-manager/internal/proxyrt"
)

// ExitInfo — публикация инстанса как выхода для маршрутизации.
// ID = идентификатор инстанса = id зеркальной записи tunnel-store
// (wdttraw-<id>), чтобы существующие ссылки в правилах не сломались (§5).
type ExitInfo struct {
	ID          string
	NDMSName    string
	KernelIface string
	Ready       bool
}

// ExitRegistry — порт реестра выходов. РЕАЛИЗАЦИЯ — ПЛАН 4 (реестр +
// каталог маршрутизации + зеркальные записи tunnel-store); здесь — контракт
// и ресурс. Регистрация — при создании, не при Ready: каталог обязан
// разрешать имя и для лежачего выхода (§5 спеки).
type ExitRegistry interface {
	Lookup(id string) (ExitInfo, bool)
	Ensure(info ExitInfo) error
}

// RoutableExit — ресурс routable_exit. ТОЛЬКО клиент: у сервера публикация
// убрана решением владельца 2026-08-17 (сервер — вход, правило-ловушка).
type RoutableExit struct {
	id   proxyrt.ResourceID
	reg  ExitRegistry
	want ExitInfo
}

func NewRoutableExit(id proxyrt.ResourceID, reg ExitRegistry) *RoutableExit {
	return &RoutableExit{id: id, reg: reg}
}

func (r *RoutableExit) SetDesired(info ExitInfo) { r.want = info }

func (r *RoutableExit) ID() proxyrt.ResourceID { return r.id }

// Observe кладёт ТОЛЬКО факты. Сравнение с желаемым — в Plan: движок зовёт
// Resources дважды за проход, наблюдение идёт МЕЖДУ вызовами, и вердикт,
// запечённый в Observe, судил бы по желаемому ПЕРВОГО вызова (I2 ревью).
// Форма та же, что у ndmsres.Address: желаемое перечитывается в момент
// планирования.
func (r *RoutableExit) Observe(context.Context) (proxyrt.Observation, error) {
	got, ok := r.reg.Lookup(r.want.ID)
	return proxyrt.Observation{Known: true, Exists: ok, Attrs: map[string]string{
		"id":           got.ID,
		"ndms_name":    got.NDMSName,
		"kernel_iface": got.KernelIface,
		"ready":        strconv.FormatBool(got.Ready),
	}}, nil
}

func (r *RoutableExit) Plan(obs proxyrt.Observation) []proxyrt.Step {
	if obs.Exists &&
		obs.Attrs["id"] == r.want.ID &&
		obs.Attrs["ndms_name"] == r.want.NDMSName &&
		obs.Attrs["kernel_iface"] == r.want.KernelIface &&
		obs.Attrs["ready"] == strconv.FormatBool(r.want.Ready) {
		return nil
	}
	return []proxyrt.Step{{Resource: r.id, Op: "publish", Reason: "запись в реестре выходов отстала"}}
}

func (r *RoutableExit) Apply(_ context.Context, s proxyrt.Step) error {
	if s.Op != "publish" {
		return fmt.Errorf("неизвестный шаг %q", s.Op)
	}
	return r.reg.Ensure(r.want)
}

func (r *RoutableExit) RecheckAfter() time.Duration { return 0 }
