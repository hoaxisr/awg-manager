package firc

import "sync"

// runtimeObserver помнит последнее увиденное сторожем состояние рантайма и
// зовёт подписчика только на его смену, а не на каждый такт: живость fircd и
// ошибка синхронизации KeenDNS меняются без участия панели (падение демона,
// облако не ответило), а страницы FIRC без таймера иначе этого не увидят.
type runtimeObserver struct {
	mu   sync.Mutex
	fn   func()
	seen bool
	// running — принятое (не сырое) состояние: одиночный сбой пинга его не
	// роняет, см. noteRuntimeState.
	running    bool
	downStreak int
	// keenDNSFailing — есть ли ошибка, а не её текст: в тексте бывают
	// адреса и порты, и сравнение по нему давало бы подсказку каждый такт.
	keenDNSFailing bool
}

// runtimeDownConfirmations — сколько подряд неудачных пингов считать
// падением демона. Один сбой бывает и у живого fircd (таймаут под нагрузкой).
const runtimeDownConfirmations = 2

// SetOnRuntimeChange задаёт подписчика на смену рантайма FIRC, замеченную
// сторожем. Вызывается до Start; подписчик не должен звать обратно в Operator
// под своими замками — он только публикует подсказку.
func (o *Operator) SetOnRuntimeChange(fn func()) {
	o.runtimeObs.mu.Lock()
	o.runtimeObs.fn = fn
	o.runtimeObs.mu.Unlock()
}

// noteRuntimeState сверяет наблюдение такта с прошлым. Первое наблюдение
// только запоминается: до него страница грузила состояние сама.
func (o *Operator) noteRuntimeState(running bool, keenDNSErr string) {
	obs := &o.runtimeObs
	obs.mu.Lock()
	if running {
		obs.downStreak = 0
	} else {
		obs.downStreak++
	}
	accepted := running
	if !running && obs.seen && obs.running && obs.downStreak < runtimeDownConfirmations {
		accepted = true // ещё не подтверждено
	}
	failing := keenDNSErr != ""
	changed := obs.seen && (obs.running != accepted || obs.keenDNSFailing != failing)
	obs.seen = true
	obs.running = accepted
	obs.keenDNSFailing = failing
	fn := obs.fn
	obs.mu.Unlock()
	if changed && fn != nil {
		fn()
	}
}
