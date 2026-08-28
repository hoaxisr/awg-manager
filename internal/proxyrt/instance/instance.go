// Package instance — сборка одного прокси-инстанса: роль + движок + связь.
// Все обязательные склейки живут здесь и только здесь: ClearEvicted на
// границе прогона, одна запись в журнал на реконсиляцию, публикация состояния.
package instance

import (
	"context"

	"github.com/hoaxisr/awg-manager/internal/proxyrt"
	"github.com/hoaxisr/awg-manager/internal/proxyrt/control"
)

// Journal — узкий срез *logging.ScopedLogger.
type Journal interface {
	Info(action, target, message string)
	Warn(action, target, message string)
}

// Config — сборка инстанса. Cfg и Intent — функции: их значения живут у
// писателя конфига (план 5) и читаются на каждом прогоне.
//
// ВЛАДЕЛЕЦ Link — инстанс: переданную связь закрывает Stop (см. его докстроку).
// Отдавать сюда связь, нужную кому-то ещё, нельзя.
//
// States и Journal ОБЯЗАТЕЛЬНЫ (G4: полные зависимости, nil-веток нет).
// Link — единственное объявленное исключение из G4 (M-4 ревью-3): это
// конкретный тип *control.Link, чей честный двойник требует живого сокета, а
// сборка Worker+Reconciler+журнал тестируется без него; nil-Link означает
// ровно «в этом тесте сокета нет», в проде все четыре роли Link имеют, и
// проводка плана 5 обязана его передавать.
type Config struct {
	ID        string
	Role      proxyrt.Role
	Cfg       func() any
	Intent    func() proxyrt.Intent
	Link      *control.Link
	States    *proxyrt.StateStore
	Journal   Journal
	MaxPasses int
}

// cfgRole подставляет роли снимок конфига: движковый Reconciler держит cfg
// константой конструктора, а наш конфиг живёт у писателя (план 5) и меняется
// между прогонами.
//
// Снимок берётся РАЗ в прогон, а не на каждый Resources. Движок держит состав
// ресурсов стабильным для пары (intent, cfg) на всём прогоне: проверку дублей
// ID он делает по первому списку, а Plan и Apply работают по второму
// (reconcile.go). Перечитай мы конфиг между этими вызовами — списки разошлись
// бы, и проверенным оказался бы не тот, который применяется.
//
// snap пишет и читает одна горутина — воркерная: движок зовёт intent и
// Resources последовательно внутри runOnce (worker.go).
type cfgRole struct {
	inner proxyrt.Role
	snap  any
}

func (c *cfgRole) Resources(intent proxyrt.Intent, _ any, obs proxyrt.Observations) []proxyrt.Resource {
	return c.inner.Resources(intent, c.snap, obs)
}

// Instance — один прокси-инстанс поверх движка.
type Instance struct {
	id     string
	cfg    Config
	worker *proxyrt.Worker
}

func New(cfg Config) *Instance {
	// Дефект проводки, не рантайма: без журнала и публикации инстанс слеп,
	// а немой отказ (nil-ветки) — ровно режим кандидата №9. Валим сборку.
	if cfg.Role == nil || cfg.Cfg == nil || cfg.Intent == nil || cfg.States == nil || cfg.Journal == nil {
		panic("instance.New: неполные зависимости (G4)")
	}
	inst := &Instance{id: cfg.ID, cfg: cfg}
	role := &cfgRole{inner: cfg.Role}
	rec := proxyrt.NewReconciler(role, nil, proxyrt.ReconcileOpts{MaxPasses: cfg.MaxPasses})
	// Замыкание намерения — единственный per-run-хук движка (worker.go зовёт
	// его один раз за прогон), поэтому снимок конфига берётся здесь же: пара
	// (intent, cfg) обязана быть одна на весь прогон.
	intent := func() proxyrt.Intent {
		role.snap = cfg.Cfg()
		return cfg.Intent()
	}
	// Идентификатора у воркера нет по построению (worker.go: «заводить второй
	// экземпляр имени незачем») — им владеет Instance и подставляет в Update.
	inst.worker = proxyrt.NewWorker(rec, intent, inst.onState)
	return inst
}

// onState — граница прогона: единственное место ClearEvicted (control/link.go:
// защёлка вытеснения не переживает реконсиляцию), одна запись в журнал
// (спека §8) и публикация состояния. Намерение НЕ перечитывается: оно едет в
// Result.Intent — публиковать пару, которой цикл не считал, движок прямо
// запрещает (докстрока reconcile.go:18-23).
func (i *Instance) onState(res proxyrt.Result, phase proxyrt.Phase) {
	if i.cfg.Link != nil { // единственное исключение из G4 — см. докстроку Config
		i.cfg.Link.ClearEvicted()
	}
	line := Summarize(res, phase)
	if phase == proxyrt.PhaseFailed || phase == proxyrt.PhaseStuck {
		i.cfg.Journal.Warn("reconcile", i.id, line)
	} else {
		i.cfg.Journal.Info("reconcile", i.id, line)
	}
	i.cfg.States.Update(i.id, res, phase)
}

// Role — декларация инстанса. Иначе состав ресурсов, собранный проводкой, не
// наблюдаем снаружи вовсе: ведомость ресурсов роли живёт за неэкспортируемым
// полем, а через воркер её видно только прогоном — то есть с настоящими
// обращениями к роутеру.
func (i *Instance) Role() proxyrt.Role { return i.cfg.Role }

func (i *Instance) Start(ctx context.Context) { i.worker.Start(ctx) }

// Post — будильник инстанса; сюда указывает LinkOpts.Post (проводка плана 5).
// Будильник несёт вид и ничего больше: воркер и есть инстанс (event.go:8-10).
func (i *Instance) Post(kind proxyrt.EventKind) bool {
	return i.worker.Post(kind)
}

// ResetStartBackoff снимает у процесса роли паузу повторного старта. Роль без
// процесса паузы не имеет — сбрасывать нечего (см. proxyrt.BackoffResetter).
func (i *Instance) ResetStartBackoff() {
	if r, ok := i.cfg.Role.(proxyrt.BackoffResetter); ok {
		r.ResetStartBackoff()
	}
}

// Stop гасит инстанс НАВСЕГДА: воркер терминален (worker.go — stopOnce), и
// второго Start у него не будет. Поэтому здесь же кончается связь: инстанс —
// единственное место, где воркер и Link лежат рядом, и без закрытия каждое
// удаление инстанса оставляло бы горутину watch и открытый unix-сокет.
//
// Порядок обязателен: сперва воркер, потом связь. onState зовёт ClearEvicted,
// и закрытие связи до возврата worker.Stop наложилось бы на идущий прогон.
// Link.Close идемпотентен, так что повторный Close у вызывающего безвреден.
func (i *Instance) Stop() {
	i.worker.Stop()
	if i.cfg.Link != nil { // единственное исключение из G4 — см. докстроку Config
		i.cfg.Link.Close()
	}
}
