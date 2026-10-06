package query

// Кредиты своих хуков (П21) — сколько хуков по имени NDMS ещё должен нам за
// наши же команды: ifcreated/ifdestroyed и грань слоя conf (running/disabled).
// Хуки NDMS приходят по имени в порядке FIFO и без потерь, но с лагом до
// минут; поэтому свой поздний хук узнаётся по счётчику выданных и ещё не
// погашенных кредитов этого имени и вида, а не по возрасту метки.
//
// Выдача — только за ребро, которого NDMS без нашей команды не даст:
// created — вход ConfirmCreated при доказанном `created` (после ответа NDMS,
// решение владельца В2); destroyed — ExpectRemoval ДО POST `no interface`;
// грань conf — ExpectConf ДО POST `up`, только действующего (стенд П8 06.10:
// действующий up:true/up:false даёт ровно одну грань conf, 20/20 и 20/20 под
// churn; no-op, `no interface` и голое создание — ни одной).
// Гашение — ClaimOwn* (−1, если кредит есть); зовёт только точка входа spool
// (api.HookSink.Handle). Новое воплощение имени кредиты не трогает: хук
// воплощения k приходит раньше хука k+1 и гасит кредит k. Запись удаляется
// при всех нулях — память ограничена именами с хуками в полёте. Времени в
// решении нет.

// hookKind — вид кредита.
type hookKind int

const (
	hookCreated hookKind = iota
	hookDestroyed
	hookConfRunning
	hookConfDisabled
	hookKinds
)

// hookCredit — непогашенные кредиты одного имени по видам.
type hookCredit [hookKinds]uint32

// ClaimOwnCreated гасит кредит created имени: true — ifcreated наш.
func (s *InterfaceStore) ClaimOwnCreated(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.takeCreditLocked(name, hookCreated)
}

// ClaimOwnDestroyed гасит кредит destroyed имени: true — ifdestroyed наш.
func (s *InterfaceStore) ClaimOwnDestroyed(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.takeCreditLocked(name, hookDestroyed)
}

// ClaimOwnConf гасит кредит грани conf=level имени: true — грань наша. Уровни,
// кроме running/disabled, кредитов не имеют.
func (s *InterfaceStore) ClaimOwnConf(name, level string) bool {
	kind, ok := confKind(level)
	if !ok {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.takeCreditLocked(name, kind)
}

// confKind — вид кредита грани conf=level.
func confKind(level string) (hookKind, bool) {
	switch level {
	case "running":
		return hookConfRunning, true
	case "disabled":
		return hookConfDisabled, true
	}
	return 0, false
}

// takeCreditLocked — кредит вида > 0 → −1 и true; иначе false. Насыщение в
// нуле: кредитов не бывает больше выданных, поэтому ноль у отказа значит
// «свой кредит уже погасил хук», а не чужой долг.
func (s *InterfaceStore) takeCreditLocked(name string, kind hookKind) bool {
	c := s.credits[name]
	if c[kind] == 0 {
		return false
	}
	c[kind]--
	if c == (hookCredit{}) {
		delete(s.credits, name)
	} else {
		s.credits[name] = c
	}
	return true
}

// grantLocked — кредит вида kind имени.
func (s *InterfaceStore) grantLocked(name string, kind hookKind) {
	c := s.credits[name]
	c[kind]++
	s.credits[name] = c
}

// ExpectConf выдаёт кредит грани conf ДО POST `interface name up:<up>` — хук
// может прийти раньше, чем вызывающий разберёт ответ. Только за действующую
// команду: запись есть в карте и её слой conf не целевой; иначе кредита нет —
// no-op грани не даёт, и кредит повис бы, поглотив потом чужую грань. Карта
// сразу принимает целевой слой: следующая команда того же имени судит по
// нашему последнему слову, а не по опоздавшему хуку; список, начатый раньше
// выдачи, слой не перетирает (confAt, applyListLocked), свой хук его не пишет
// (диспетчер, Event.Own). refused — POST отказан или не дошёл: кредит
// снимается насыщенно, слой возвращается, карта грязная (следующий читатель
// перечитает список).
func (s *InterfaceStore) ExpectConf(name string, up bool) (refused func()) {
	level, kind := "disabled", hookConfDisabled
	if up {
		level, kind = "running", hookConfRunning
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.byID[name]
	if !ok || rec.ConfLayer == level {
		return func() {}
	}
	prev := rec.ConfLayer
	s.grantLocked(name, kind)
	rec.ConfLayer = level
	s.seq++
	s.confAt[name] = s.seq
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.takeCreditLocked(name, kind)
		if rec, ok := s.byID[name]; ok && rec.ConfLayer == level {
			rec.ConfLayer = prev
		}
		delete(s.confAt, name)
		s.seq++
		s.dirtyAt = s.seq
	}
}

// ExpectRemoval выдаёт кредит destroyed ДО POST `no interface name`: хук может
// прийти раньше, чем вызывающий разберёт ответ. Исход ответа — ровно один
// метод жетона. Единственный вызывающий — command.deleteInterface (сканер
// TestExpectRemoval_OnlyInDeleteInterface).
func (s *InterfaceStore) ExpectRemoval(name string) *RemovalToken {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.grantLocked(name, hookDestroyed)
	return &RemovalToken{s: s, name: name}
}

// RemovalToken — один наш `no interface`; исход — один из Removed, Absent,
// Refused (повторный — паника). Карту при снятии чистит только он.
type RemovalToken struct {
	s    *InterfaceStore
	name string
	done bool
}

func (t *RemovalToken) use() {
	if t.done {
		panic("query: RemovalToken " + t.name + ": жетон уже использован")
	}
	t.done = true
}

// Removed — NDMS снял запись: карта забывает её сразу (метка gone без
// «грязно»), кредит остаётся — его погасит наш ifdestroyed.
func (t *RemovalToken) Removed() {
	t.use()
	t.s.mu.Lock()
	defer t.s.mu.Unlock()
	t.s.forgetLocked(t.name)
}

// Absent — «unable to find»: записи уже не было. Карта забывает её, кредит
// снимается — ifdestroyed от нашей команды не будет.
func (t *RemovalToken) Absent() {
	t.use()
	t.s.mu.Lock()
	defer t.s.mu.Unlock()
	t.s.forgetLocked(t.name)
	t.s.takeCreditLocked(t.name, hookDestroyed)
}

// Refused — отказ или транспортная ошибка: карта не тронута, кредит снимается.
func (t *RemovalToken) Refused() {
	t.use()
	t.s.mu.Lock()
	defer t.s.mu.Unlock()
	t.s.takeCreditLocked(t.name, hookDestroyed)
}
