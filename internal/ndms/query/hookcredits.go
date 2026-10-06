package query

// Кредиты своих хуков существования (П21): сколько ifcreated/ifdestroyed по
// имени NDMS ещё должен нам за наши же команды. Хуки NDMS приходят по имени в
// порядке FIFO и без потерь, но с лагом до минут; поэтому свой поздний хук
// узнаётся по счётчику выданных и ещё не погашенных кредитов этого имени и
// вида, а не по возрасту метки.
//
// Выдача — только за ребро, которого NDMS без нашей команды не даст:
// created — вход ConfirmCreated при доказанном `created` (после ответа NDMS,
// решение владельца В2); destroyed — ExpectRemoval ДО POST `no interface`.
// Гашение — ClaimOwnCreated/ClaimOwnDestroyed (−1, если кредит есть).
// Новое воплощение имени кредиты не трогает: хук воплощения k приходит раньше
// хука k+1 и гасит кредит k. Запись удаляется при обоих нулях — память
// ограничена именами с хуками в полёте. Времени в решении нет.

// hookCredit — непогашенные кредиты одного имени.
type hookCredit struct{ created, destroyed uint32 }

// ClaimOwnCreated гасит кредит created имени: true — ifcreated наш.
func (s *InterfaceStore) ClaimOwnCreated(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.takeCreditLocked(name, true)
}

// ClaimOwnDestroyed гасит кредит destroyed имени: true — ifdestroyed наш.
func (s *InterfaceStore) ClaimOwnDestroyed(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.takeCreditLocked(name, false)
}

// takeCreditLocked — кредит вида > 0 → −1 и true; иначе false. Насыщение в
// нуле: кредитов не бывает больше выданных, поэтому ноль у исхода жетона
// значит «свой кредит уже погасил хук», а не чужой долг.
func (s *InterfaceStore) takeCreditLocked(name string, created bool) bool {
	c := s.credits[name]
	n := &c.destroyed
	if created {
		n = &c.created
	}
	if *n == 0 {
		return false
	}
	*n--
	if c == (hookCredit{}) {
		delete(s.credits, name)
	} else {
		s.credits[name] = c
	}
	return true
}

// grantCreatedLocked — кредит created: NDMS доказанно создал запись нашей
// командой (вход ConfirmCreated при created).
func (s *InterfaceStore) grantCreatedLocked(name string) {
	c := s.credits[name]
	c.created++
	s.credits[name] = c
}

// ExpectRemoval выдаёт кредит destroyed ДО POST `no interface name`: хук может
// прийти раньше, чем вызывающий разберёт ответ. Исход ответа — ровно один
// метод жетона. Единственный вызывающий — command.deleteInterface (сканер
// TestExpectRemoval_OnlyInDeleteInterface).
func (s *InterfaceStore) ExpectRemoval(name string) *RemovalToken {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.credits[name]
	c.destroyed++
	s.credits[name] = c
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
	t.s.takeCreditLocked(t.name, false)
}

// Refused — отказ или транспортная ошибка: карта не тронута, кредит снимается.
func (t *RemovalToken) Refused() {
	t.use()
	t.s.mu.Lock()
	defer t.s.mu.Unlock()
	t.s.takeCreditLocked(t.name, false)
}
