package proxyrt

import "context"

// OwnedResource — ресурс роутера, помеченный нашей меткой владения.
type OwnedResource struct {
	Label string
	Name  string
}

// Scanner перечисляет ресурсы роутера с нашими метками. Меток несколько:
// у клиента и сервера они разные. Граница доверия: уборщик полагается на то,
// что Scanner вернул только помеченное нами, и сам метки не перепроверяет.
type Scanner interface {
	Scan(ctx context.Context, labels []string) ([]OwnedResource, error)
}

// Remover сносит ресурс.
type Remover interface {
	Remove(ctx context.Context, r OwnedResource) error
}

// Sweeper удаляет то, что помечено нашей меткой, но не объявлено ни одним
// живым инстансом. Это единственный путь удаления ресурсов: воркеры инстансов
// ничего не сносят.
type Sweeper struct {
	sc     Scanner
	rm     Remover
	alloc  *Allocator
	labels []string
}

func NewSweeper(sc Scanner, rm Remover, alloc *Allocator, labels []string) *Sweeper {
	return &Sweeper{sc: sc, rm: rm, alloc: alloc, labels: labels}
}

// Sweep сносит невостребованное. declared — имена ресурсов, объявленных живыми
// декларациями; выключенный инстанс тоже объявляет свои ресурсы и потому
// защищён.
//
// Скан упал — не сносим ничего: «не знаем» не равно «наш и лишний».
//
// Решение о списке принимается под локом аллокатора, а сами сносы идут вне
// его: снос — это RCI-вызовы на секунды, и держать на них общий лок значит
// остановить выделение номеров всем инстансам сразу.
func (s *Sweeper) Sweep(ctx context.Context, declared map[string]bool) ([]string, error) {
	found, err := s.sc.Scan(ctx, s.labels)
	if err != nil {
		return nil, err
	}

	var doomed []OwnedResource
	s.alloc.WithLock(func() {
		for _, r := range found {
			if !declared[r.Name] {
				doomed = append(doomed, r)
			}
		}
	})

	var removed []string
	var firstErr error
	for _, r := range doomed {
		if rmErr := s.rm.Remove(ctx, r); rmErr != nil {
			if firstErr == nil {
				firstErr = rmErr
			}
			continue
		}
		removed = append(removed, r.Name)
	}
	return removed, firstErr
}
