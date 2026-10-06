package command

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

// ErrNotCreated — на создание NDMS не ответил `"name" interface created.`:
// запись name уже была, команда её лишь настроила (F574). Для имени,
// выбранного свободным по свежему списку, это чужая запись, которую NDMS ещё
// не показал ни списком, ни хуком: не наша — ни настроек, ни сноса.
var ErrNotCreated = errors.New("NDMS не создал запись: имя уже занято")

// ErrLeftOnRouter — запись, созданная этой командой и не подтверждённая
// списком, не снесена: снос отказал. Она осталась на роутере голой (F577).
var ErrLeftOnRouter = errors.New("созданная запись осталась на роутере")

// CreateReply — что ответ NDMS на команду создания говорит о записи (F574).
// Фраза ответа одна на всех версиях прошивки (R56). Нулевое значение —
// CreateNotNew: «не доказано» не может появиться по умолчанию.
type CreateReply int

const (
	// CreateNotNew — создания ответ не доказал: запись уже была (чужая или
	// наша) и команда её лишь настроила, либо POST провалился и ответа нет.
	CreateNotNew CreateReply = iota
	// CreateNew — в ответе `"name" interface created.`: запись создана ЭТОЙ
	// командой.
	CreateNew
)

// Proven — создание доказано ответом NDMS (`"name" interface created.`):
// только такую запись вызывающий вправе сносить по имени при откате, и только
// её, не показанную списком, сносит confirmCreated.
func (r CreateReply) Proven() bool { return r == CreateNew }

// PostCreate — POST команды, создающей name (с настройками или без), с
// разбором ответа как у PostChecked и вердиктом о записи (CreateReply).
func PostCreate(ctx context.Context, p Poster, payload any, opDesc, name string, after ...func()) (CreateReply, error) {
	resp, err := postChecked(ctx, p, payload, opDesc, nil, after...)
	if err == nil && replySaysCreated(resp, name) {
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
