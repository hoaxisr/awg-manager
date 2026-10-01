package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
	"github.com/hoaxisr/awg-manager/internal/sys/osdetect"
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
// created — CreateReply.Ours() ответа на создание (у импорта — всегда true).
// Без этого сноса нет вовсе (F574): команда попала в уже существующую
// запись, и если это чужая, ещё не показанная списком, снос удалил бы её.
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

// CreateReply — что ответ NDMS на команду создания говорит о записи (F574).
type CreateReply int

const (
	// CreateLegacy — прошивка, где фраза ответа стендом не подтверждена
	// (<5.01): поведение как до F574 — запись считается нашей.
	CreateLegacy CreateReply = iota
	// CreateNew — в ответе `"name" interface created.`: запись создана ЭТОЙ
	// командой.
	CreateNew
	// CreateNotNew — фраза проверяема, но создания она не доказала: запись
	// уже была (чужая или наша) и команда её лишь настроила, либо POST
	// провалился и ответа нет.
	CreateNotNew
)

// Ours — вправе ли вызывающий сносить запись по имени при откате: всё, кроме
// не доказанной созданной на прошивке, где доказательство есть.
func (r CreateReply) Ours() bool { return r != CreateNotNew }

// createdReplyProven — фраза `"X" interface created.` (code 6553601) в ответе
// на создание снята стендом только на 5.01 (R39). На старших она та же, на
// младших (4.x, 5.00) не проверена: там без неё каждое создание давало бы
// ErrNotCreated и сироту, поэтому ответ не разбирается вовсе.
func createdReplyProven() bool { return osdetect.AtLeast(5, 1) }

// PostCreate — POST команды, создающей name (с настройками или без), с
// разбором ответа как у PostChecked и вердиктом о записи (CreateReply).
func PostCreate(ctx context.Context, p Poster, payload any, opDesc, name string, after ...func()) (CreateReply, error) {
	resp, err := postChecked(ctx, p, payload, opDesc, nil, after...)
	switch {
	case !createdReplyProven():
		return CreateLegacy, err
	case err == nil && replySaysCreated(resp, name):
		return CreateNew, nil
	}
	return CreateNotNew, err
}

// replySaysCreated — есть ли где-либо в ответе message с `"name" interface
// created`. Подстрока, а не префикс: в журнальной форме перед ней стоит ident
// (`Network::Interface::Repository: "X" interface created.`); кавычки не дают
// спутать Wireguard1 с Wireguard10.
func replySaysCreated(resp json.RawMessage, name string) bool {
	var root any
	if json.Unmarshal(resp, &root) != nil {
		return false
	}
	return hasCreatedMessage(root, `"`+name+`" interface created`)
}

func hasCreatedMessage(v any, want string) bool {
	switch t := v.(type) {
	case map[string]any:
		if m, ok := t["message"].(string); ok && strings.Contains(m, want) {
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
// подтверждение (F574, F584). NDMS доказанно не создал запись (CreateNotNew)
// — ErrNotCreated без подтверждения, настроек и сноса, если только
// вызывающий не принимает существующую запись осознанно (existingOK: managed
// restore в живой сервер того же ключа). Вердикт — вызывающему: откат вправе
// сносить только Ours.
func CreateInterface(ctx context.Context, p Poster, save *SaveCoordinator, q *query.Queries, payload any, name string, existingOK bool, after ...func()) (query.Confirmed, CreateReply, error) {
	reply, err := PostCreate(ctx, p, payload, "create "+name, name, after...)
	if err != nil {
		return query.Confirmed{}, reply, err
	}
	if reply == CreateNotNew && !existingOK {
		return query.Confirmed{}, reply, fmt.Errorf("%w: %s", ErrNotCreated, name)
	}
	c, err := ConfirmCreated(ctx, p, save, q, name, reply.Ours())
	return c, reply, err
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
