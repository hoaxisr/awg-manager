package proxyrt

import (
	"context"
	"errors"
)

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

// IndexOf разбирает имя ресурса в номер. Передаётся вызывающим: движок не
// знает про именование NDMS. Вернул false — номер неизвестен, ресурс
// рассматривается только по declared.
//
// Зовётся ПОД локом аллокатора: решение о сносе консультирует held. Значит
// разбор имени обязан быть чистым — никаких обращений к аллокатору (лок не
// реентрантен, будет дедлок) и никакого ввода-вывода: на время разбора встаёт
// выделение номеров всем инстансам сразу.
type IndexOf func(name string) (int, bool)

// Sweeper удаляет то, что помечено нашей меткой, но не объявлено ни одним
// живым инстансом. Это единственный путь удаления ресурсов: воркеры инстансов
// ничего не сносят.
type Sweeper struct {
	sc      Scanner
	rm      Remover
	alloc   *Allocator
	labels  []string
	indexOf IndexOf
}

// NewSweeper паникует на пустом списке меток: уборщик — единственный путь
// удаления, и без меток он молча не удалял бы ничего никогда, накапливая сирот.
// Это ошибка программирования, а не режим работы.
//
// По той же причине паникует и на nil-разборщике имени и nil-аллокаторе: без
// них Sweep упал бы уже посреди уборки, между решением о сносе и самими
// сносами, а не при сборке движка.
func NewSweeper(sc Scanner, rm Remover, alloc *Allocator, labels []string, indexOf IndexOf) *Sweeper {
	if len(labels) == 0 {
		panic("proxyrt: NewSweeper без меток владения — уборщик не удалял бы ничего")
	}
	if indexOf == nil {
		panic("proxyrt: NewSweeper без разборщика имени в номер — Sweep упал бы посреди уборки")
	}
	if alloc == nil {
		panic("proxyrt: NewSweeper без аллокатора — решение о сносе принимается под его локом")
	}
	return &Sweeper{sc: sc, rm: rm, alloc: alloc, labels: labels, indexOf: indexOf}
}

// Sweep сносит невостребованное. declared — имена ресурсов, объявленных живыми
// декларациями; выключенный инстанс тоже объявляет свои ресурсы и потому
// защищён.
//
// Скан упал — не сносим ничего: «не знаем» не равно «наш и лишний».
//
// Метка каждой записи перепроверяется по своему же списку: довод «сканер вернул
// только наше» верен ровно до бага в сканере, а цена бага здесь — снесённый
// чужой интерфейс роутера.
//
// Отмена контекста прекращает сносы и отказом уборки не считается — так же,
// как отмена отделена от отказа в цикле реконсиляции и воркере.
//
// Решение о списке принимается под локом аллокатора, а сами сносы идут вне
// его: снос — это RCI-вызовы на секунды, и держать на них общий лок значит
// остановить выделение номеров всем инстансам сразу.
//
// Под локом решение консультирует held аллокатора: ресурс приговаривается,
// только если он не в declared И его номер не закреплён ни за кем. Иначе
// остаётся гонка, ради которой лок и заявлен: инстанс получил номер и создал
// интерфейс, а declared собран вызывающим до того, как инстанс успел
// объявиться, — и свежесозданный интерфейс был бы снесён.
func (s *Sweeper) Sweep(ctx context.Context, declared map[string]bool) ([]string, error) {
	found, err := s.sc.Scan(ctx, s.labels)
	if err != nil {
		return nil, err
	}

	ours := make(map[string]bool, len(s.labels))
	for _, l := range s.labels {
		ours[l] = true
	}

	var doomed []OwnedResource
	s.alloc.WithLock(func(isHeld func(int) bool) {
		for _, r := range found {
			if !ours[r.Label] || declared[r.Name] {
				continue
			}
			if idx, ok := s.indexOf(r.Name); ok && isHeld(idx) {
				continue // номер закреплён за живым владельцем
			}
			doomed = append(doomed, r)
		}
	})

	var removed []string
	var firstErr error
	for _, r := range doomed {
		if ctx.Err() != nil {
			break
		}
		if rmErr := s.rm.Remove(ctx, r); rmErr != nil {
			// Отмена — не отказ уборки, откуда бы она ни пришла: и из состояния
			// контекста, и возвратом из самого Remove.
			if ctx.Err() != nil || errors.Is(rmErr, context.Canceled) {
				break
			}
			if firstErr == nil {
				firstErr = rmErr
			}
			continue
		}
		removed = append(removed, r.Name)
	}
	return removed, firstErr
}
