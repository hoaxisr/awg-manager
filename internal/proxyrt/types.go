// Package proxyrt — движок реконсиляции прокси-инстансов: наблюдение,
// планирование, применение. Роли и специфика NDMS/iptables сюда не попадают.
package proxyrt

import "time"

// ResourceID — стабильный идентификатор ресурса внутри инстанса.
type ResourceID string

// Status — состояние одного ресурса по результату наблюдения и применения.
type Status string

const (
	StatusOK Status = "ok" // фактическое совпадает с желаемым
	// StatusDrift — расхождение, для которого есть шаги. Инвариант: drift
	// означает, что для ресурса есть шаги, то есть план не пуст.
	StatusDrift   Status = "drift"
	StatusBlocked Status = "blocked" // предшественник в цепочке не пришёл в норму
	StatusUnknown Status = "unknown" // наблюдение недоступно; шагов не порождает
	StatusFailed  Status = "failed"  // применение отказало с объяснимой причиной
)

// Phase — состояние инстанса целиком, выводится из ресурсов (см. DerivePhase).
type Phase string

const (
	PhaseSettled  Phase = "settled"
	PhaseWaiting  Phase = "waiting"
	PhaseFailed   Phase = "failed"
	PhaseStuck    Phase = "stuck"
	PhaseDisabled Phase = "disabled"
)

// Intent — намерение владельца инстанса. Параметр декларации, а не только
// раскраска состояния: при disabled роль объявляет другой состав ресурсов.
type Intent string

// IntentDeleted внутри движка ведёт себя как IntentDisabled (та же ведомость,
// то же пустое желаемое, та же фаза), потому что снос NDMS — работа уборщика по
// меткам, а не ветки декларации (§4.2). Отсюда ДВА обязательства проводки
// (план 5), которых движок исполнить не может:
//
//  1. конфиг удаляемого инстанса убирается из списка, по которому строится
//     instance.DeclaredNDMSNames. Ведомость уборщика — это список конфигов, и
//     намерения она не знает: инстанс, оставленный в списке с deleted, вечно
//     объявляет свои имена и блокирует уборку СВОИХ ЖЕ ресурсов;
//  2. на удалении зовётся Allocator.Release(owner) — иначе номера OpkgTun и
//     listen-порты не возвращаются в пул (выключенный инстанс номер держит
//     намеренно, удалённый — нет).
const (
	IntentEnabled  Intent = "enabled"
	IntentDisabled Intent = "disabled"
	IntentDeleted  Intent = "deleted"
)

// StopReason — почему цикл реконсиляции прекратил проходы.
type StopReason string

const (
	// StopNone — стопор не сработал: цикл дошёл до пустого плана либо ещё идёт.
	// На путях отказа причина остановки не выставляется — её несёт фаза.
	StopNone     StopReason = ""
	StopAwaiting StopReason = "awaiting" // новых шагов нет, ждём эффекта применённых
	StopCeiling  StopReason = "ceiling"  // исчерпан потолок проходов
	StopCanceled StopReason = "canceled" // отменён контекст: выключение демона
)

// Step — единица плана. Данные, а не замыкание: план печатается, сравнивается
// в тестах и показывается пользователю.
type Step struct {
	Resource ResourceID
	Op       string
	Args     map[string]string
	Reason   string
}

// Observation — результат наблюдения ресурса.
//
// Known=false означает «не смотрели» и обязано читаться как unknown, а не как
// «ресурса нет»: иначе слепое наблюдение породит create.
//
// Exists и Attrs — данные для самого ресурса и его роли: по ним Plan решает
// «создать или поправить». Движок их не смотрит, он читает только Known,
// Detail и Public.
type Observation struct {
	Known  bool
	Exists bool
	Detail string
	// Attrs — СЛУЖЕБНЫЕ атрибуты наблюдения: их читают роль и Plan. В
	// состояние ресурса они не попадают. Так и задумано: сюда попадают
	// величины вроде uptime_s, меняющиеся каждый прогон, и публикация
	// состояния по ним шла бы на каждый будильник.
	Attrs map[string]string
	// Public — атрибуты, которые роль хочет показать пользователю: копируются
	// в ResourceState и уходят в API. Движок их не читает.
	Public map[string]string
}

// ResourceState — то, что видит пользователь и API по каждому ресурсу.
type ResourceState struct {
	ID     ResourceID
	Status Status
	Detail string
	Error  string
	// Attrs — наблюдение ресурса для показа пользователю (ndms_access:
	// foreign-acl); копия Observation.Public, движок не читает. Служебные
	// Observation.Attrs сюда НЕ копируются.
	Attrs map[string]string
}

// InstanceState — состояние инстанса наружу.
type InstanceState struct {
	ID        string
	Intent    Intent
	Phase     Phase
	Resources []ResourceState
	LastPlan  []Step
	UpdatedAt time.Time
}
