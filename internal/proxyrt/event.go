package proxyrt

// EventKind — что разбудило реконсиляцию. Все события ведут в одну функцию и
// для воркера равнозначны: событие — будильник, а не команда. Различаются они
// для журнала и для тех, кто их ставит (планы 2-5); перечень источников —
// спека §6.
//
// Будильник — это вид и ничего больше. Идентификатор инстанса в него не входит:
// событие кладут конкретному воркеру, а воркер и есть инстанс, и второе поле
// могло бы с ним разойтись.
type EventKind string

const (
	EventIntentChanged EventKind = "intent-changed" // API поменял намерение
	EventBoot          EventKind = "boot"
	EventProcessState  EventKind = "process-state" // push от дочернего процесса
	EventProcessDied   EventKind = "process-died"
	EventNDMSHook      EventKind = "ndms-hook" // ifcreated/ifdestroyed/iflayerchanged
	EventWANUp         EventKind = "wan-up"
	EventWANDown       EventKind = "wan-down"
	EventPolicyChanged EventKind = "policy-changed" // accesspolicy через наш API
	EventRecheck       EventKind = "recheck"        // сработал таймер подстраховки
)
