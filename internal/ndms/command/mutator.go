package command

import (
	"context"
	"errors"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// ndmsMutator — общее ядро групп команд, которые создают и сносят записи
// интерфейсов (Interface, Proxy, Wireguard): poster, координатор сохранений и
// кэши. Создание (createInterface/confirmCreated) и снос (deleteInterface) —
// его методы, поэтому все три группы идут одним путём без ссылок друг на
// друга, а координатор приходит только из конструктора (П24).
type ndmsMutator struct {
	poster  Poster
	save    *SaveCoordinator
	queries *query.Queries
}

// newMutator — единственный nil-гард координатора: без него `no interface`
// ушёл бы мимо сохранения, а паника случилась бы далеко от причины.
func newMutator(p Poster, s *SaveCoordinator, q *query.Queries) ndmsMutator {
	if s == nil {
		panic("command: SaveCoordinator обязателен")
	}
	return ndmsMutator{poster: p, save: s, queries: q}
}

// confirmCreated подтверждает только что созданный интерфейс name
// (query.ConfirmCreated: ограниченное ожидание записи в свежем списке, F584).
// Записи так и нет, а создание доказано ответом NDMS (created:
// query.ErrNotListed) — снос `no interface name` через deleteInterface по
// query.Unlisted: без него интерфейс оставался сиротой. Создание не доказано
// (ErrNotSeen: запись уже была) — ошибка без сноса: `no interface` по
// отсутствующему пишет E в журнал ndm (стенд 5.01.C.6), а неснесённую сироту
// покажет следующий список (Occupied / системные туннели).
//
// Это ЕДИНСТВЕННОЕ место, где имя без списка становится Confirmed (сканеры
// TestNotListed_OnlyInConfirmCreated, TestUnlisted_OnlyInNotListedSite).
// Контракт Confirmed это не нарушает: он защищает от `interface X …` по
// отсутствующему X, которое СОЗДАЁТ X, а `no interface X` ничего не создаёт;
// шлётся только при доказанном создании, а отказ «unable to find interface»
// (запись успели снять) терпим. Имя — из нашей же принятой команды создания
// или из ответа NDMS на импорт (`created`).
//
// Список не прочитан (решение 4) — ошибка без команд: созданное остаётся на
// роутере, его найдёт следующий список, как любую запись без туннеля.
//
// created — CreateReply.Proven() ответа на создание (у импорта — всегда true).
// Без этого сноса нет вовсе (F574): команда попала в уже существующую
// запись, и если это чужая, ещё не показанная списком, снос удалил бы её.
func (m *ndmsMutator) confirmCreated(ctx context.Context, name string, created bool) (query.Confirmed, error) {
	conf, err := m.queries.Interfaces.ConfirmCreated(ctx, name, created)
	if !created || !errors.Is(err, query.ErrNotListed) {
		return conf, err
	}
	var after []func()
	if m.queries.RunningConfig != nil {
		after = append(after, m.queries.RunningConfig.InvalidateAll)
	}
	if derr := m.deleteInterface(ctx, query.Unlisted(name), "delete unlisted "+name, after...); derr != nil {
		return query.Confirmed{}, errors.Join(err, fmt.Errorf("%w: %w", ErrLeftOnRouter, derr))
	}
	return query.Confirmed{}, fmt.Errorf("%w; созданная запись снесена", err)
}

// createInterface — создание name, выбранного свободным (FreeIndex), и его
// подтверждение (F574, F584). NDMS доказанно не создал запись (CreateNotNew)
// — ErrNotCreated без подтверждения, настроек и сноса, если только
// вызывающий не принимает существующую запись осознанно (existingOK: managed
// restore в живой сервер того же ключа). Вердикт — вызывающему: откат вправе
// сносить только Proven.
func (m *ndmsMutator) createInterface(ctx context.Context, payload any, name string, existingOK bool, after ...func()) (query.Confirmed, CreateReply, error) {
	reply, err := PostCreate(ctx, m.poster, payload, "create "+name, name, after...)
	if err != nil {
		return query.Confirmed{}, reply, err
	}
	if reply == CreateNotNew && !existingOK {
		return query.Confirmed{}, reply, fmt.Errorf("%w: %s", ErrNotCreated, name)
	}
	c, err := m.confirmCreated(ctx, name, reply.Proven())
	return c, reply, err
}
