package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// ConfirmCreated подтверждает только что созданный интерфейс name
// (query.ConfirmCreated: ограниченное ожидание записи в свежем списке, F584).
// Записи так и нет, но NDMS её знает (по имени пришёл хук, ifdestroyed не
// было: query.ErrNotListed) — снос `no interface name`: без него интерфейс
// оставался сиротой, которую снести нечем (снос требует Confirmed). Следа нет
// (ErrNotSeen) или имя уже снято (ErrCreatedThenRemoved) — ошибка без сноса:
// `no interface` по отсутствующему пишет E в журнал ndm (стенд 5.01.C.6), а
// неснесённую сироту покажет следующий список (Occupied / системные туннели).
//
// Это ЕДИНСТВЕННАЯ команда по интерфейсу без Confirmed (исключение под
// TestNotListed_OnlyInConfirmCreated). Контракт Confirmed она не нарушает:
// он защищает от `interface X …` по отсутствующему X, которое СОЗДАЁТ X, а
// `no interface X` ничего не создаёт; шлётся только при следе записи, а
// отказ «unable to find interface» (запись успели снять) терпим. Имя — из нашей же принятой команды
// создания или из ответа NDMS на импорт (`created`), запись — наша, созданная
// этим же потоком миллисекунды назад.
//
// Список не прочитан (решение 4) — ошибка без команд: созданное остаётся на
// роутере, его найдёт следующий список, как любую запись без туннеля.
//
// created — ответ на создание сказал `"name" interface created.` (PostCreate;
// у импорта — всегда). Без этого сноса нет вовсе (F574): команда попала в уже
// существующую запись, и если это чужая, ещё не показанная списком, снос
// удалил бы её.
func ConfirmCreated(ctx context.Context, p Poster, save *SaveCoordinator, q *query.Queries, name string, created bool) (query.Confirmed, error) {
	return confirmCreated(ctx, p, save, q, nil, name, created)
}

// ConfirmCreated — ConfirmCreated пакета по командам интерфейсов; снос
// неподтверждённого заранее объявлен оркестратору (свой ifdestroyed).
func (c *InterfaceCommands) ConfirmCreated(ctx context.Context, name string, created bool) (query.Confirmed, error) {
	return confirmCreated(ctx, c.poster, c.save, c.queries, c.hookNotifier, name, created)
}

// ErrNotCreated — на создание NDMS не ответил `"name" interface created.`:
// запись name уже была, команда её лишь настроила (F574). Для имени,
// выбранного свободным по свежему списку, это чужая запись, которую NDMS ещё
// не показал ни списком, ни хуком: не наша — ни настроек, ни сноса.
var ErrNotCreated = errors.New("NDMS не создал запись: имя уже занято")

// PostCreate — POST команды, создающей name (с настройками или без), с
// разбором ответа как у PostChecked; created — NDMS создал запись ЭТОЙ
// командой: в ответе есть `"name" interface created.` (стенд 5.01, code
// 6553601). По уже существующей записи этого сообщения нет.
func PostCreate(ctx context.Context, p Poster, payload any, opDesc, name string, after ...func()) (created bool, err error) {
	resp, err := postChecked(ctx, p, payload, opDesc, nil, after...)
	if err != nil {
		return false, err
	}
	var root any
	if json.Unmarshal(resp, &root) != nil {
		return false, nil
	}
	return hasCreatedMessage(root, `"`+name+`" interface created`), nil
}

// hasCreatedMessage — есть ли где-либо в ответе message, начинающийся с want.
func hasCreatedMessage(v any, want string) bool {
	switch t := v.(type) {
	case map[string]any:
		if m, ok := t["message"].(string); ok && strings.HasPrefix(m, want) {
			return true
		}
		for _, val := range t {
			if hasCreatedMessage(val, want) {
				return true
			}
		}
	case []any:
		for _, val := range t {
			if hasCreatedMessage(val, want) {
				return true
			}
		}
	}
	return false
}

// CreateInterface — создание name, выбранного свободным (FreeIndex), и его
// подтверждение (F574, F584). NDMS не создал запись (ErrNotCreated) —
// ошибка без подтверждения, настроек и сноса, если только вызывающий не
// принимает существующую запись осознанно (existingOK: managed restore в
// живой сервер того же ключа).
func CreateInterface(ctx context.Context, p Poster, save *SaveCoordinator, q *query.Queries, payload any, name string, existingOK bool, after ...func()) (query.Confirmed, error) {
	created, err := PostCreate(ctx, p, payload, "create "+name, name, after...)
	if err != nil {
		return query.Confirmed{}, err
	}
	if !created && !existingOK {
		return query.Confirmed{}, fmt.Errorf("%w: %s", ErrNotCreated, name)
	}
	return ConfirmCreated(ctx, p, save, q, name, created)
}

func confirmCreated(ctx context.Context, p Poster, save *SaveCoordinator, q *query.Queries, hn HookNotifier, name string, created bool) (query.Confirmed, error) {
	conf, err := q.Interfaces.ConfirmCreated(ctx, name)
	if !created || !errors.Is(err, query.ErrNotListed) {
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
