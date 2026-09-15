package proxyrt

import (
	"errors"
	"sync"
)

// ErrNoFreePort — в диапазоне не осталось свободных портов.
var ErrNoFreePort = errors.New("нет свободного порта в диапазоне")

// PortRange — окно выдачи LISTEN-ПОРТОВ (roles.ListenPortMin/Max).
//
// Тип и его операции назывались «индексом», пока этот же аллокатор раздавал ещё
// и номера OpkgTun. Номера ушли в общий пул (internal/opkgtun), а имена остались
// и стали врать: раздаётся ровно одно — порт.
type PortRange struct {
	Min int
	Max int
}

// Allocator раздаёт LISTEN-ПОРТЫ клиентских инстансов из пространства, общего
// для всех. «Один воркер на инстанс» сериализует только внутри инстанса,
// поэтому выделение обязано идти под собственным локом — иначе два воркера
// одновременно увидят один и тот же свободный порт.
//
// Номера OpkgTun он больше не раздаёт: их выдаёт общий пул (internal/opkgtun),
// потому что пул делят четыре подсистемы, и отдельная очередь у каждой
// означала бы отсутствие атомарности.
type Allocator struct {
	mu   sync.Mutex
	rng  PortRange
	held map[int]string // порт → идентификатор владельца
}

func NewAllocator(r PortRange) *Allocator {
	return &Allocator{rng: r, held: make(map[int]string)}
}

// AllocPort выдаёт порт владельцу. pinned+havePin — «сохрани этот, если он
// годен»: порт стоит в конфиге клиента снаружи, и молчаливый переезд рвёт
// соединение. Негодный (вне диапазона либо занятый чужим владельцем) меняется
// на свободный — прежде такой порт был приговором, инстанс уходил в blocked и
// сам оттуда не выбирался.
//
// taken — занятость СНАРУЖИ аллокатора: порты, стоящие в записях на диске.
// Собственные порты владельца вычитает вызывающий: без этого владелец,
// переиспользующий свой порт, получил бы другой.
//
// Ключ владельца — key+"/listen", а не голый key: Release освобождает ВСЕ
// порты владельца, и возврат после listen-аллокации отобрал бы у живой записи
// её порт.
func (a *Allocator) AllocPort(owner string, pinned int, havePin bool, taken map[int]bool) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// Диапазон проверяется здесь, и это ЕДИНСТВЕННОЕ место, где он проверяется:
	// окно принадлежит аллокатору, и копия правила у вызывающего разошлась бы с
	// ним. Без проверки негодный по диапазону пин выдавался бы как есть —
	// вопреки обещанию ниже и вопреки валидатору ссылки абонента, который такой
	// порт отвергает (roles/linkres).
	free := func(i int) bool {
		if i < a.rng.Min || i > a.rng.Max {
			return false
		}
		h, ok := a.held[i]
		return (!ok || h == owner) && !taken[i]
	}
	grant := func(i int) int {
		for port, h := range a.held {
			if h == owner && port != i {
				delete(a.held, port)
			}
		}
		a.held[i] = owner
		return i
	}

	if havePin && free(pinned) {
		return grant(pinned), nil
	}
	for i := a.rng.Min; i <= a.rng.Max; i++ {
		if free(i) {
			return grant(i), nil
		}
	}
	return 0, ErrNoFreePort
}

// Release освобождает все порты владельца. Зовётся только при удалении
// инстанса: выключенный инстанс держит порт, чтобы ссылка абонента не повисла
// на чужом.
//
// Освобождение именно по владельцу, а не по порту: иначе остался бы способ
// освободить чужой порт.
func (a *Allocator) Release(owner string) {
	a.mu.Lock()
	for port, h := range a.held {
		if h == owner {
			delete(a.held, port)
		}
	}
	a.mu.Unlock()
}
