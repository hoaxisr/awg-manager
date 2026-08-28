package ndmsres

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hoaxisr/awg-manager/internal/proxyrt"
)

// Iface — ресурс ndms_interface: существование + описание + security-level +
// MTU. Адрес и admin state — отдельные ресурсы: у них другое desired при
// disabled и другой источник желаемого.
type Iface struct {
	id   proxyrt.ResourceID
	cmds Commands
	q    Query
	d    IfaceDesired
}

// IfaceDesired: Present=false (disabled) означает «нет ЛИБО есть» — интерфейс
// не создаётся и не удаляется; удаление — только sweeper при deleted.
//
// MTU — стартовое значение ТОЛЬКО для create. Владелец MTU — ресурс
// ndms_address: у raw-клиента MTU приходит от сервера (RAWCONF), и два
// владельца с разными значениями давали бы вечный drift — Iface ставит
// константу, Address перетирает фактом, и так каждый прогон (I2 ревью;
// старый код не конфликтовал: prepare ставил MTU один раз ДО RAWCONF).
type IfaceDesired struct {
	Present       bool
	Name          string
	Description   string
	SecurityLevel string
	MTU           int // только для create; дрейф MTU чинит ndms_address
}

func NewIface(id proxyrt.ResourceID, cmds Commands, q Query) *Iface {
	return &Iface{id: id, cmds: cmds, q: q}
}

func (r *Iface) SetDesired(d IfaceDesired) { r.d = d }

func (r *Iface) ID() proxyrt.ResourceID { return r.id }

func (r *Iface) Observe(ctx context.Context) (proxyrt.Observation, error) {
	facts, ok, err := r.q.Iface(ctx, r.d.Name)
	if err != nil {
		return proxyrt.Observation{}, err
	}
	return proxyrt.Observation{
		Known: true, Exists: ok,
		Attrs: map[string]string{
			"description":    facts.Description,
			"security_level": facts.SecurityLevel,
			"mtu":            strconv.Itoa(facts.MTU),
		},
		Detail: r.d.Name,
	}, nil
}

func (r *Iface) Plan(obs proxyrt.Observation) []proxyrt.Step {
	if !r.d.Present {
		// Ничего: существование при disabled законно, отсутствие — тоже.
		return nil
	}
	if !obs.Exists {
		return []proxyrt.Step{{Resource: r.id, Op: "create",
			Args: map[string]string{"name": r.d.Name}, Reason: "интерфейса нет в NDMS"}}
	}
	var steps []proxyrt.Step
	add := func(op, reason string) {
		steps = append(steps, proxyrt.Step{Resource: r.id, Op: op,
			Args: map[string]string{"name": r.d.Name}, Reason: reason})
	}
	if obs.Attrs["description"] != r.d.Description {
		add("set-description", "описание не наше")
	}
	if obs.Attrs["security_level"] != r.d.SecurityLevel {
		add("set-security-level", "уровень безопасности не тот")
	}
	// MTU здесь НЕ сверяется: владелец MTU — ndms_address (см. IfaceDesired).
	return steps
}

func (r *Iface) Apply(ctx context.Context, s proxyrt.Step) error {
	switch s.Op {
	case "create":
		// Уровень задаётся сразу при создании — лишняя мутация RCI на
		// каждом старте не нужна (паритет prepareOpkgTunIface).
		if err := r.cmds.CreateOpkgTunWithSecurityLevel(ctx, r.d.Name, r.d.Description, r.d.SecurityLevel); err != nil {
			return err
		}
		return r.cmds.SetMTU(ctx, r.d.Name, r.d.MTU)
	case "set-description":
		return r.cmds.SetDescription(ctx, r.d.Name, r.d.Description)
	case "set-security-level":
		return r.cmds.SetSecurityLevel(ctx, r.d.Name, r.d.SecurityLevel)
	default:
		return fmt.Errorf("неизвестный шаг %q", s.Op)
	}
}

func (r *Iface) RecheckAfter() time.Duration { return 0 }

// AddrWant — желаемый адрес.
type AddrWant struct {
	Address string
	Mask    string
	MTU     int
}

// AddressDesired: либо Clear (disabled/мёртвый процесс — адрес снят), либо
// Want-колбэк. Колбэк, а не значение: у raw-клиента адрес выдаёт сервер, и
// роль читает его из свежего снимка процесса в момент наблюдения.
type AddressDesired struct {
	Name  string
	Clear bool
	Want  func() (AddrWant, bool, string) // (want, известен ли, почему нет)
}

// Address — ресурс ndms_address.
type Address struct {
	id   proxyrt.ResourceID
	cmds Commands
	q    Query
	d    AddressDesired
}

func NewAddress(id proxyrt.ResourceID, cmds Commands, q Query) *Address {
	return &Address{id: id, cmds: cmds, q: q}
}

func (r *Address) SetDesired(d AddressDesired) { r.d = d }

func (r *Address) ID() proxyrt.ResourceID { return r.id }

func (r *Address) Observe(ctx context.Context) (proxyrt.Observation, error) {
	if !r.d.Clear {
		if _, known, why := r.want(); !known {
			// Желаемое неизвестно (RAWCONF ещё не пришёл): waiting с
			// названным триггером — push address от процесса.
			return proxyrt.Observation{Known: false, Detail: why}, nil
		}
	}
	facts, ok, err := r.q.Iface(ctx, r.d.Name)
	if err != nil {
		return proxyrt.Observation{}, err
	}
	return proxyrt.Observation{
		Known: true, Exists: ok,
		Attrs: map[string]string{
			"address": facts.Address,
			"mask":    facts.Mask,
			"mtu":     strconv.Itoa(facts.MTU),
			"broken":  strconv.FormatBool(facts.Broken),
		},
	}, nil
}

func (r *Address) want() (AddrWant, bool, string) {
	if r.d.Want == nil {
		return AddrWant{}, false, "желаемый адрес не задан"
	}
	return r.d.Want()
}

func (r *Address) Plan(obs proxyrt.Observation) []proxyrt.Step {
	if !obs.Exists {
		// Интерфейса нет: адресовать его нельзя (create-on-reference).
		// Создание — забота ndms_interface; следующий прогон доведёт адрес.
		return nil
	}
	if r.d.Clear {
		if obs.Attrs["broken"] == "true" {
			// Запись в состоянии error — устройства нет, и снятие адреса
			// роутер отвергает («system failed [0xcffd0217]»). Шаг повторялся
			// бы на каждом прогоне (стенд 2026-08-28), а адрес и так никуда
			// не ведёт: тот же довод, что у admin state ниже.
			return nil
		}
		if obs.Attrs["address"] != "" {
			return []proxyrt.Step{{Resource: r.id, Op: "clear-address",
				Args:   map[string]string{"name": r.d.Name},
				Reason: "инстанс выключен: NDMS-адрес без kernel-адреса вешает RCI (PR #544)"}}
		}
		return nil
	}
	want, _, _ := r.want()
	var steps []proxyrt.Step
	if obs.Attrs["address"] != want.Address || obs.Attrs["mask"] != want.Mask {
		steps = append(steps, proxyrt.Step{Resource: r.id, Op: "set-address",
			Args:   map[string]string{"name": r.d.Name, "address": want.Address, "mask": want.Mask},
			Reason: "адрес не тот"})
	}
	if want.MTU > 0 && obs.Attrs["mtu"] != strconv.Itoa(want.MTU) {
		steps = append(steps, proxyrt.Step{Resource: r.id, Op: "set-mtu",
			Args:   map[string]string{"name": r.d.Name, "mtu": strconv.Itoa(want.MTU)},
			Reason: "mtu из наблюдения процесса"})
	}
	return steps
}

func (r *Address) Apply(ctx context.Context, s proxyrt.Step) error {
	switch s.Op {
	case "set-address":
		return r.cmds.SetAddress(ctx, r.d.Name, s.Args["address"], s.Args["mask"])
	case "set-mtu":
		mtu, err := strconv.Atoi(s.Args["mtu"])
		if err != nil {
			return err
		}
		return r.cmds.SetMTU(ctx, r.d.Name, mtu)
	case "clear-address":
		return ignoreMissingObject(r.cmds.ClearAddress(ctx, r.d.Name))
	default:
		return fmt.Errorf("неизвестный шаг %q", s.Op)
	}
}

func (r *Address) RecheckAfter() time.Duration { return 0 }

// AdminDesired — желаемый admin state.
type AdminDesired struct {
	Name string
	Up   bool
}

// AdminState — ресурс ndms_admin_state.
type AdminState struct {
	id   proxyrt.ResourceID
	cmds Commands
	q    Query
	d    AdminDesired
}

func NewAdminState(id proxyrt.ResourceID, cmds Commands, q Query) *AdminState {
	return &AdminState{id: id, cmds: cmds, q: q}
}

func (r *AdminState) SetDesired(d AdminDesired) { r.d = d }

func (r *AdminState) ID() proxyrt.ResourceID { return r.id }

func (r *AdminState) Observe(ctx context.Context) (proxyrt.Observation, error) {
	facts, ok, err := r.q.Iface(ctx, r.d.Name)
	if err != nil {
		return proxyrt.Observation{}, err
	}
	return proxyrt.Observation{Known: true, Exists: ok,
		Attrs: map[string]string{
			"up":     strconv.FormatBool(facts.AdminUp),
			"broken": strconv.FormatBool(facts.Broken),
		}}, nil
}

func (r *AdminState) Plan(obs proxyrt.Observation) []proxyrt.Step {
	if !obs.Exists {
		// `up false` на несуществующем интерфейсе СОЗДАЁТ его
		// (create-on-reference, PR #734) — не трогаем ни up, ни down.
		return nil
	}
	up := obs.Attrs["up"] == "true"
	if up == r.d.Up {
		return nil
	}
	if !r.d.Up && obs.Attrs["broken"] == "true" {
		// Опускать нечего: запись в состоянии error — устройства за ней нет.
		// Роутер такую команду ОТВЕРГАЕТ («OpkgTun0: system failed
		// [0xcffd01b9]»), а шаг планировался бы на каждом прогоне: стенд
		// 2026-08-28 дал 21 повтор за семь минут и не собирался
		// останавливаться. Цель «не поднят» при этом уже достигнута.
		return nil
	}
	op := "down"
	if r.d.Up {
		op = "up"
	}
	return []proxyrt.Step{{Resource: r.id, Op: op,
		Args: map[string]string{"name": r.d.Name}, Reason: "admin state не тот"}}
}

func (r *AdminState) Apply(ctx context.Context, s proxyrt.Step) error {
	switch s.Op {
	case "up":
		return r.cmds.InterfaceUp(ctx, r.d.Name)
	case "down":
		return ignoreMissingObject(r.cmds.InterfaceDown(ctx, r.d.Name))
	default:
		return fmt.Errorf("неизвестный шаг %q", s.Op)
	}
}

func (r *AdminState) RecheckAfter() time.Duration { return 0 }

// ignoreMissingObject гасит отказ роутера на снятии того, чего уже нет.
//
// `clear address` и `interface down` по записи, за которой не осталось
// устройства, роутер отвергает как `system failed [0xcffd0217]` /
// `[0xcffd01b9]` — проверено curl'ом на стенде 5.01.C.3.0-1. Для этих двух
// операций отказ означает, что цель УЖЕ достигнута: адреса нет, интерфейс не
// поднят. Считать это ошибкой — значит писать в журнал пугающую строку на
// каждой остановке инстанса и планировать шаг заново, пока NDMS не проставит
// записи state error (наблюдение выше ловит только уже проставленный).
//
// Гасится ровно эта форма отказа: остальные ошибки RCI (нет связи, отказ
// разбора) доходят до вызывающего.
func ignoreMissingObject(err error) error {
	if err == nil || strings.Contains(err.Error(), "system failed") {
		return nil
	}
	return err
}
