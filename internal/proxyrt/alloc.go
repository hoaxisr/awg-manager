package proxyrt

import (
	"errors"
	"sync"
)

// ErrNoFreeIndex — в диапазоне не осталось свободных номеров.
var ErrNoFreeIndex = errors.New("нет свободного индекса в диапазоне")

// IndexRange — диапазон номеров интерфейсов. Для wdtt это OpkgTun17..49:
// 0..9 занят fakeip, 10..16 — awg, 100+ — managed.
type IndexRange struct {
	Min int
	Max int
}

// Allocator раздаёт номера из пространства, общего для всех инстансов обеих
// подсистем. «Один воркер на инстанс» сериализует только внутри инстанса,
// поэтому выделение обязано идти под собственным локом — иначе два воркера
// одновременно увидят один и тот же свободный номер.
//
// Тем же локом пользуется уборщик: ресурс без живой декларации не принадлежит
// ни одному воркеру.
type Allocator struct {
	mu   sync.Mutex
	rng  IndexRange
	held map[int]bool
}

func NewAllocator(r IndexRange) *Allocator {
	return &Allocator{rng: r, held: make(map[int]bool)}
}

// AllocIndex выдаёт номер. pinned — ранее закреплённый за инстансом номер: его
// стоит вернуть, пока он свободен, потому что пользователь мог сослаться на имя
// интерфейса в permit'ах политики. taken — номера, занятые снаружи.
func (a *Allocator) AllocIndex(pinned int, taken map[int]bool) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	free := func(i int) bool { return !a.held[i] && !taken[i] }

	if pinned >= a.rng.Min && pinned <= a.rng.Max && free(pinned) {
		a.held[pinned] = true
		return pinned, nil
	}
	for i := a.rng.Min; i <= a.rng.Max; i++ {
		if free(i) {
			a.held[i] = true
			return i, nil
		}
	}
	return 0, ErrNoFreeIndex
}

// Release возвращает номер в оборот. Зовётся только при intent=deleted:
// выключенный инстанс держит свой номер, чтобы permit'ы пользователя не
// повисли на чужом имени.
func (a *Allocator) Release(idx int) {
	a.mu.Lock()
	delete(a.held, idx)
	a.mu.Unlock()
}

// WithLock выполняет f под тем же локом, что и выделение номеров. Нужен
// уборщику: решение «этот ресурс лишний» и выделение номера не должны идти
// одновременно.
func (a *Allocator) WithLock(f func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	f()
}
