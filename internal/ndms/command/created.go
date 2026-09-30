package command

import (
	"context"
	"errors"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// ConfirmCreated подтверждает только что созданный интерфейс name
// (query.ConfirmCreated: ограниченное ожидание записи в свежем списке, F584).
// Записи так и нет — снос `no interface name`: без него интерфейс оставался
// сиротой, которую снести нечем (снос требует Confirmed).
//
// Это ЕДИНСТВЕННАЯ команда по интерфейсу без Confirmed (исключение под
// TestNotListed_OnlyInConfirmCreated). Контракт Confirmed она не нарушает:
// он защищает от `interface X …` по отсутствующему X, которое СОЗДАЁТ X, а
// `no interface X` ничего не создаёт — по отсутствующему это отказ «unable to
// find interface», терпимый здесь. Имя — из нашей же принятой команды
// создания или из ответа NDMS на импорт (`created`), запись — наша, созданная
// этим же потоком миллисекунды назад.
//
// Список не прочитан (решение 4) — ошибка без команд: созданное остаётся на
// роутере, его найдёт следующий список, как любую запись без туннеля.
func ConfirmCreated(ctx context.Context, p Poster, save *SaveCoordinator, q *query.Queries, name string) (query.Confirmed, error) {
	return confirmCreated(ctx, p, save, q, nil, name)
}

// ConfirmCreated — ConfirmCreated пакета по командам интерфейсов; снос
// неподтверждённого заранее объявлен оркестратору (свой ifdestroyed).
func (c *InterfaceCommands) ConfirmCreated(ctx context.Context, name string) (query.Confirmed, error) {
	return confirmCreated(ctx, c.poster, c.save, c.queries, c.hookNotifier, name)
}

func confirmCreated(ctx context.Context, p Poster, save *SaveCoordinator, q *query.Queries, hn HookNotifier, name string) (query.Confirmed, error) {
	conf, err := q.Interfaces.ConfirmCreated(ctx, name)
	if !errors.Is(err, query.ErrNotListed) {
		return conf, err
	}
	var after []func()
	if save != nil {
		after = append(after, save.Request)
	}
	if q.RunningConfig != nil {
		after = append(after, q.RunningConfig.InvalidateAll)
	}
	if hn != nil {
		hn.ExpectHook(name, "destroyed")
	}
	drop := map[string]any{"interface": map[string]any{name: map[string]any{"no": true}}}
	if derr := PostChecked(ctx, p, drop, "delete unlisted "+name, isMissingInterface, after...); derr != nil {
		return query.Confirmed{}, errors.Join(err, derr)
	}
	q.Interfaces.Forget(name)
	return query.Confirmed{}, fmt.Errorf("%w; созданная запись снесена", err)
}
