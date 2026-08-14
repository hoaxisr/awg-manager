package proxyrt

import (
	"context"
	"time"
)

// Resource — единица владения и сверки. Знает только про себя: соседей не
// видит, порядок применения задаётся списком роли.
type Resource interface {
	ID() ResourceID
	// Observe возвращает фактическое состояние. Ошибка либо Known=false
	// означают «не смогли посмотреть» и дают StatusUnknown, а не «ресурса нет».
	Observe(ctx context.Context) (Observation, error)
	// Plan — чистая функция: по наблюдению возвращает шаги, ничего не делая.
	// Класть в Step.Args ту же карту, что пришла в Observation.Attrs, нельзя:
	// план — неизменяемые данные, общая ссылка сделала бы его изменяемым.
	Plan(obs Observation) []Step
	// Apply — единственное место с побочными эффектами.
	Apply(ctx context.Context, s Step) error
	// RecheckAfter — период подстраховочной сверки. 0 = только по событиям.
	RecheckAfter() time.Duration
}

// Role — декларация состава инстанса. Порядок ресурсов значим: список работает
// как цепочка, ресурс после неисправного получает StatusBlocked.
//
// Намерение — параметр, а не внешняя раскраска: при disabled роль объявляет
// «интерфейс без адреса и down», а не тот же состав, что при enabled. Иначе
// намерение пришлось бы дублировать внутрь конфига.
//
// Желаемое зависит и от наблюдений: адрес raw-клиента выдаёт сервер, поэтому
// он не может быть намерением.
type Role interface {
	Resources(intent Intent, cfg any, obs Observations) []Resource
}

// Observations — снимок наблюдений одного прохода. Обёртка над картой:
// методы с value-приёмником, карта общая, копия структуры видит те же данные.
type Observations struct {
	m map[ResourceID]observed
}

type observed struct {
	seen   bool
	obs    Observation
	err    error
	failed string
}

func NewObservations() Observations {
	return Observations{m: make(map[ResourceID]observed)}
}

// Put кладёт результат наблюдения. Ошибка сохраняется целиком: причина
// недоступности обязана доехать до состояния ресурса.
func (o Observations) Put(id ResourceID, obs Observation, err error) {
	rec := o.m[id]
	rec.seen = true
	rec.obs, rec.err = obs, err
	o.m[id] = rec
}

// MarkFailed помечает ресурс отказавшим при применении. Метка живёт до конца
// прохода; следующий проход наблюдает заново и даёт шагу ещё один шанс.
func (o Observations) MarkFailed(id ResourceID, reason string) {
	rec := o.m[id]
	rec.failed = reason
	o.m[id] = rec
}

// Get возвращает наблюдение и ошибку по ресурсу.
func (o Observations) Get(id ResourceID) (Observation, error) {
	rec := o.m[id]
	return rec.obs, rec.err
}
