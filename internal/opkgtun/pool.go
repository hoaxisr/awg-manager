package opkgtun

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
)

// maxCeiling — самый высокий потолок среди архитектур. Берётся из канонической
// карты (map.go), а не числом: копий карты быть не должно.
var maxCeiling = Ceiling("arm64")

// kernelWindowStart — начало исторического окна kernel-туннелей. С него
// начинают перебор все, кроме режимов роутера: встречные потоки расходятся, и
// нижняя половина достаётся тому, кому отступать некуда (см. order).
const kernelWindowStart = 10

// Pool — общий пул номеров.
//
// Два замка с разной работой. pick — очередь на ВЫБОР: его держит один
// Reserve от снимка занятости до записи выданного, и ждать его надо по
// контексту, поэтому это канал, а не мьютекс (мьютекс не ждётся — не
// «упрощать»). mu закрывает только карту reserved и никогда не держится
// через ввод-вывод. Порядок захвата — pick, потом mu; Close берёт ТОЛЬКО mu,
// иначе висел бы за владельцем pick, ушедшим в RCI на секунды.
type Pool struct {
	pick     chan struct{}
	mu       sync.Mutex
	reserved map[int]*Reservation
	ceiling  int
	src      []Source
}

// NewPool. Потолок считает проводка (CeilingForHost): это факт прошивки, и
// значение вне 0..maxCeiling означает ошибку сборки, а не режим работы.
//
// Нулевой потолок валиден и означает пул из одного номера — и этот номер
// достаётся ТОЛЬКО режиму роутера: пол остальных равен minIndex, и им такой
// пул пуст. Это следствие карты, а не отдельное правило.
//
// Паника, а не ошибка: всё перечисленное — дефект проводки, который обязан
// падать на старте, а не отказом пользователю посреди работы.
func NewPool(ceiling int, src ...Source) *Pool {
	if ceiling < 0 || ceiling > maxCeiling {
		panic(fmt.Sprintf("opkgtun: потолок %d вне 0..%d", ceiling, maxCeiling))
	}
	if len(src) == 0 {
		panic("opkgtun: пул без источников занятости: пустая занятость неотличима от «всё свободно»")
	}
	for _, s := range src {
		if s.Name == "" || s.Read == nil {
			panic("opkgtun: источник занятости без имени или без чтения")
		}
	}
	return &Pool{
		pick:     make(chan struct{}, 1),
		reserved: map[int]*Reservation{},
		ceiling:  ceiling,
		src:      slices.Clone(src),
	}
}

// Request — заявка на один номер.
type Request struct {
	self      Holder
	pin       int
	hasPin    bool
	strictPin bool
	veto      Taken
}

// Want — любой свободный номер.
func Want(self Holder) Request { return Request{self: self} }

// WantPinned — сначала этот номер, и только если он не достался — перебор.
// Пин нужен тем, на чьё ИМЯ ссылается настройка пользователя: permit в
// политике доступа стоит на OpkgTun12, и молчаливый переезд на другой номер
// оборвал бы его.
//
// Паникует на держателе без ключа: годность пина проверяется в том числе по
// sameOwner, а у безключевого «своё» неотличимо от чужого — такая заявка
// молча отобрала бы чужой номер.
func WantPinned(self Holder, n int) Request {
	if self.anonymous() {
		panic("opkgtun: пин у держателя без ключа: своё от чужого неотличимо")
	}
	return Request{self: self, pin: n, hasPin: true}
}

// WantPinnedStrict — пин, который НЕ перебивает анонима: номер отдаётся, только
// если КАЖДЫЙ его держатель — тот же ключ (в пределе держателей нет вовсе).
//
// Правило именно по держателю, а не по номеру: под собственной записью
// просителя может лежать чужой след, и «номер свободен» о нём ничего не
// говорит. В формулировке «свободен у всех ИЛИ держится тем же ключом» баг и
// прожил — см. occupancy.
//
// Разница с WantPinned несущая. Обычный пин отбирает номер у безключевого
// держателя — живого устройства и записи NDMS, — и это верно ровно для того,
// кто доказал, что след на номере ЕГО: режим роутера сверкой описания,
// посев своим прежним конфигом. Проситель, доказавший лишь то, что номер
// когда-то был записан за ним, такого права не имеет: чужая запись NDMS на
// том же номере — не его след, а создание интерфейса поверх неё переписывает
// чужие настройки (RCI создаёт интерфейс upsert-ом).
func WantPinnedStrict(self Holder, n int) Request {
	r := WantPinned(self, n)
	r.strictPin = true
	return r
}

// Excluding — номера, которые этому просителю запрещены, даже если в пуле они
// свободны. АБСОЛЮТНОЕ вето: оно выше и «аноним», и «свой» — то есть пин его
// тоже не обходит.
//
// Нужно ровно одному просителю: у kernel-туннеля идентификатор записи и номер
// OpkgTun — одно и то же N, и номер, свободный в пуле, может быть занят
// ИДЕНТИФИКАТОРОМ (легаси NativeWG в kernel-окне, #891). Выдать такой номер
// значит получить ErrAlreadyExists на записи.
func (r Request) Excluding(ids Taken) Request {
	r.veto = ids
	return r
}

// Reservation — выданные номера. Живёт от Reserve до Close и на это время
// держит номера занятыми для всех остальных — в том числе до того, как
// вызывающий записал их на диск. Ставит Close defer-ом тот, кто пишет; Close
// зовётся на ЛЮБОМ исходе, включая успешный: после записи номер держит уже
// запись.
type Reservation struct {
	p         *Pool
	numbers   []int
	holders   []Holder // параллелен numbers: чтобы соседу было кого назвать
	conflicts Conflicts
	closed    atomic.Bool
}

// Numbers — выданные номера В ПОРЯДКЕ ЗАЯВОК; len равен числу заявок. Копия:
// внутренний срез не отдаём, чтобы «не мутировать» не было договорённостью.
func (r *Reservation) Numbers() []int {
	if r == nil {
		return nil
	}
	return slices.Clone(r.numbers)
}

// Conflicts — номера, заявленные дважды. Неизменны после Reserve; выбору они
// уже помешали, вызывающему остаётся сказать о них в журнал.
func (r *Reservation) Conflicts() Conflicts {
	if r == nil {
		return nil
	}
	return r.conflicts
}

// Close отпускает номера. Nil-безопасен (Reserve мог вернуть nil вместе с
// ошибкой, а defer стоит до её проверки), идемпотентен, и снимает только СВОИ
// записи — сверка по тождеству резервации, а не по номеру: иначе поздний Close
// снял бы номер, уже выданный другому.
func (r *Reservation) Close() {
	if r == nil || !r.closed.CompareAndSwap(false, true) {
		return
	}
	r.p.mu.Lock()
	for _, n := range r.numbers {
		if r.p.reserved[n] == r {
			delete(r.p.reserved, n)
		}
	}
	r.p.mu.Unlock()
}

func (r *Reservation) holderOf(n int) Holder {
	for i, got := range r.numbers {
		if got == n {
			return r.holders[i]
		}
	}
	return Holder{}
}

// ErrExhausted — свободного номера нет. Разбирается только через errors.Is:
// поля отказа читает человек, а не программа.
var ErrExhausted = errors.New("нет свободного номера OpkgTun")

// Reserve выдаёт по номеру на заявку — все или ни одного.
//
// Ноль заявок — пустая резервация БЕЗ ввода-вывода, pick не берётся: штатный
// Update прокси ничего не выделяет, и заставлять вызывающего помнить об этом
// значит однажды получить лишний обход RCI в горячем пути.
//
// Порядок: ctx.Err() → паники на дефектах заявок → захват pick по ctx.Done()
// (отмена даёт ctx.Err(), а НЕ ErrExhausted: «передумали» и «пул полон» — это
// разные новости) → defer отпуска pick → под mu ранний срез reserved → чтение
// источников → выбор → под mu запись выданного.
//
// Ранний срез не избыточен: сосед мог записать свою запись на диск уже ПОСЛЕ
// того, как мы прочли источники, и закрыть резервацию — без среза его номер
// невидим обоим. Позднего среза не нужно: пока мы держим pick, reserved умеет
// только сжиматься (добавляет в него один лишь Reserve). Цена односторонняя:
// номер, отпущенный во время чтения, пропускается один раз — на почти полном
// пуле это ложный отказ.
//
// Свободен ⟺ не в veto И не в занятости И не в раннем срезе И не выдан в ЭТОМ
// вызове. Фазы две: сперва честятся пины, затем отвергнутые пины и беспиновые
// идут в перебор в порядке заявок — иначе отвергнутый пин успел бы занять
// номер, которого ждёт пин следующей заявки.
func (p *Pool) Reserve(ctx context.Context, reqs ...Request) (*Reservation, error) {
	if len(reqs) == 0 {
		return &Reservation{p: p}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	assertUniqueOwners(reqs)

	select {
	case p.pick <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	// ЕДИНСТВЕННОЕ место отпускания: выходов отсюда пять, и паника — шестой.
	// Забытый отпуск встаёт не аллокатором, а всей поверхностью прокси и
	// обоими режимами роутера — до перезапуска демона.
	defer func() { <-p.pick }()

	early := p.earlySnapshot()

	parts := make([]Taken, 0, len(p.src))
	for _, s := range p.src {
		got, err := s.Read(ctx)
		if err != nil {
			return nil, fmt.Errorf("занятость OpkgTun (%s): %w", s.Name, err)
		}
		parts = append(parts, got)
	}
	occ, conflicts := mergeAll(parts)

	conflicted := make(map[int]bool, len(conflicts))
	for _, c := range conflicts {
		conflicted[c.Index] = true
	}

	granted := Taken{}
	free := func(n int) bool {
		_, held := early[n]
		_, mine := granted[n]
		return !occ.busy(n) && !held && !mine
	}

	out := make([]int, len(reqs))
	pending := make([]int, 0, len(reqs))

	// Фаза 1: пины.
	for i, r := range reqs {
		if !r.hasPin || !p.pinGranted(r, occ, early, granted, conflicted) {
			pending = append(pending, i)
			continue
		}
		out[i] = r.pin
		granted[r.pin] = r.self
	}

	// Фаза 2: отвергнутые пины и беспиновые.
	for _, i := range pending {
		r := reqs[i]
		n, ok := p.choose(r, free)
		if !ok {
			return nil, p.exhaustedFor(r, occ.collapse(), early, conflicts)
		}
		out[i] = n
		granted[n] = r.self
	}

	res := &Reservation{p: p, numbers: out, conflicts: conflicts}
	res.holders = make([]Holder, len(reqs))
	for i, r := range reqs {
		res.holders[i] = r.self
	}
	p.mu.Lock()
	for _, n := range out {
		p.reserved[n] = res
	}
	p.mu.Unlock()
	return res, nil
}

// pinGranted — годен ли заявленный номер.
//
// Проверяется КАЖДЫЙ держатель номера, а не схлопнутый победитель: под
// собственной записью просителя может лежать чужой след, и увидеть его иначе
// нельзя (см. occupancy).
//
// Аноним отдаётся обычному пину намеренно: живое устройство и запись NDMS
// ключа не имеют, а владение на этих путях доказывает сам вызывающий (режим
// роутера — описанием интерфейса, посев — своим прежним конфигом). Тот, кто
// доказал лишь собственную прошлую запись, просит строгий пин — тому не
// отдаётся ни один чужой след. Конфликтный номер не отдаётся никому: две
// записи спорят о нём, и отдать его третьему значит назначить победителя
// жребием. Свой заявленный номер открыт ТОЛЬКО пину — в переборе он занят,
// иначе владелец забрал бы его у себя же второй заявкой.
func (p *Pool) pinGranted(r Request, occ occupancy, early, granted Taken, conflicted map[int]bool) bool {
	n := r.pin
	if n < 0 || n > p.ceiling || conflicted[n] {
		return false
	}
	if _, vetoed := r.veto[n]; vetoed {
		return false
	}
	if _, mine := granted[n]; mine {
		return false
	}
	if _, held := early[n]; held {
		return false
	}
	for _, h := range occ[n] {
		if h.sameOwner(r.self) {
			continue
		}
		if r.strictPin || !h.anonymous() {
			return false
		}
	}
	return true
}

func (p *Pool) choose(r Request, free func(int) bool) (int, bool) {
	for _, n := range p.order(r.self) {
		if _, vetoed := r.veto[n]; vetoed {
			continue
		}
		if free(n) {
			return n, true
		}
	}
	return 0, false
}

// order — порядок перебора для этого просителя.
//
// Режимы роутера идут снизу, от нуля; остальные — с kernelWindowStart вверх, а
// затем вниз по нижней половине. Встречные потоки расходятся, и номера 0..1
// достаются только режимам роутера: это решение владельца (#891) — режимов
// роутера в любой момент работает один, а туннели и прокси имеют запас
// доверху, поэтому перебор снизу голодил бы именно того, кому отступать
// некуда. Нижняя половина у остальных идёт позже своего окна, но раньше
// чужого: так kernel уходит в верхнее окно прокси последним.
//
// Резерв 0..1 — правило ВЫДАЧИ, а не владения: уже существующая запись на
// этих номерах свой номер сохраняет, её защищает занятость.
func (p *Pool) order(self Holder) []int {
	out := make([]int, 0, p.ceiling+1)
	if self.kind == kindRouterMode {
		for n := 0; n <= p.ceiling; n++ {
			out = append(out, n)
		}
		return out
	}
	for n := kernelWindowStart; n <= p.ceiling; n++ {
		out = append(out, n)
	}
	// Обрезка потолком обязательна: без неё пул с потолком меньше девяти
	// выдавал бы номера ВЫШЕ потолка, и выбор разошёлся бы с пином — тот
	// потолок проверяет. В проде потолок всегда 16 или 49, но расхождение
	// выбора и пина — это и есть класс #891.
	for n := min(kernelWindowStart-1, p.ceiling); n >= minIndex; n-- {
		out = append(out, n)
	}
	return out
}

func (p *Pool) floorFor(self Holder) int {
	if self.kind == kindRouterMode {
		return 0
	}
	return minIndex
}

func (p *Pool) earlySnapshot() Taken {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.reserved) == 0 {
		return nil
	}
	out := make(Taken, len(p.reserved))
	for n, res := range p.reserved {
		out[n] = res.holderOf(n)
	}
	return out
}

// assertUniqueOwners — две заявки с одним непустым ключом невыразимы: годность
// пина решает sameOwner, и на дубле ключа обе заявки признали бы номер друг
// друга своим. Зовётся ДО захвата pick: паника под ним встала бы очередью.
//
// С диска сюда попасть нельзя: единственный многозаявочный вызов — посев, и он
// дедуплицирует записи раньше, чем строит заявки.
func assertUniqueOwners(reqs []Request) {
	seen := make(map[string]bool, len(reqs))
	for _, r := range reqs {
		if r.self.k == "" {
			continue
		}
		if seen[r.self.k] {
			panic("opkgtun: две заявки с одним ключом владельца: " + r.self.k)
		}
		seen[r.self.k] = true
	}
}
