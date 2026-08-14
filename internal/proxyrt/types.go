// Package proxyrt — движок реконсиляции прокси-инстансов: наблюдение,
// планирование, применение. Роли и специфика NDMS/iptables сюда не попадают.
package proxyrt

import "time"

// ResourceID — стабильный идентификатор ресурса внутри инстанса.
type ResourceID string

// Status — состояние одного ресурса по результату наблюдения и применения.
type Status string

const (
	StatusOK      Status = "ok"      // фактическое совпадает с желаемым
	StatusDrift   Status = "drift"   // расхождение, для которого есть шаги
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

const (
	IntentEnabled  Intent = "enabled"
	IntentDisabled Intent = "disabled"
	IntentDeleted  Intent = "deleted"
)

// StopReason — почему цикл реконсиляции прекратил проходы.
type StopReason string

const (
	StopNone     StopReason = ""         // цикл дошёл до пустого плана
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
type Observation struct {
	Known  bool
	Exists bool
	Detail string
	Attrs  map[string]string
}

// ResourceState — то, что видит пользователь и API по каждому ресурсу.
type ResourceState struct {
	ID     ResourceID
	Status Status
	Detail string
	Error  string
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
