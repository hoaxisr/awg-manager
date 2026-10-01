package command

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// postMutationChecked — базовый путь всех мутаций NDMS. Роутер отвечает
// HTTP 200 и на отказ тоже, пряча его во вложенном status[]: транспортная
// проверка такой ответ пропускает, поэтому проваленный шаг рапортовал бы об
// успехе, а падало бы через шаг-другой и с чужой формулировкой (issue #768).
// Найдя status:"error" или получив транспортную ошибку, вызов возвращает
// ошибку; save и инвалидация идут в ОБОИХ случаях — см. postMutationCheckedTolerant.
//
// Формы ответов сняты с живого роутера (KeeneticOS 5.01): повторные
// create/set/up/down отвечают message, ошибку даёт настоящий отказ. Список
// отказов, безобидных для идемпотентных сносов, — в tolerate.go.
func postMutationChecked(
	ctx context.Context,
	poster Poster,
	save *SaveCoordinator,
	payload any,
	opDesc string,
	invalidators ...func(),
) error {
	return postMutationCheckedTolerant(ctx, poster, save, payload, opDesc, nil, invalidators...)
}

// postMutationCheckedTolerant — postMutationChecked, у которого часть отказов
// признаётся безобидной. Нужен идемпотентным сносам: NDMS отвечает
// status:"error" на удаление того, чего уже нет, и без этого реап и откат
// падали бы на собственной повторной попытке. Прочие отказы всплывают.
func postMutationCheckedTolerant(
	ctx context.Context,
	poster Poster,
	save *SaveCoordinator,
	payload any,
	opDesc string,
	tolerate func(msg string) bool,
	invalidators ...func(),
) error {
	return PostChecked(ctx, poster, payload, opDesc, tolerate,
		append([]func(){save.Request}, invalidators...)...)
}

// PostChecked — разбор ответа postMutationCheckedTolerant для пакетов, которые
// собирают payload сами (managed): транспортная ошибка или вложенный
// status:"error" (кроме терпимых tolerate) — ошибка. after (save, инвалидация)
// вызывается ПОСЛЕ POST на ОБОИХ путях — см. комментарий в теле.
func PostChecked(
	ctx context.Context,
	poster Poster,
	payload any,
	opDesc string,
	tolerate func(msg string) bool,
	after ...func(),
) error {
	_, err := postChecked(ctx, poster, payload, opDesc, tolerate, after...)
	return err
}

// postChecked — PostChecked, отдающий ответ NDMS (PostCreate читает из него
// «interface created»).
func postChecked(
	ctx context.Context,
	poster Poster,
	payload any,
	opDesc string,
	tolerate func(msg string) bool,
	after ...func(),
) (json.RawMessage, error) {
	resp, err := poster.Post(ctx, payload)
	// Инвалидация и сохранение идут и по путям отказа. status:"error": NDMS
	// применяет пакетный payload поэлементно, отвергнутый элемент не отменяет
	// уже применённые. Транспортная ошибка (таймаут, обрыв): RCI мог применить
	// payload, а ответ — не доехать; допущение «таймаут = ничего не применено»
	// неверно, и применённое иначе не попало бы в startup-config. Лишний save
	// на сбое коалесцируется SaveCoordinator (debounce/maxWait).
	for _, f := range after {
		f()
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", opDesc, err)
	}
	if msgs := ndmsStatusErrors(resp); len(msgs) > 0 && !allTolerated(msgs, tolerate) {
		return nil, fmt.Errorf("%s: router reported error: %s", opDesc, strings.Join(msgs, "; "))
	}
	return resp, nil
}

// allTolerated: терпим ответ, только если КАЖДЫЙ отказ в нём признан
// безобидным — иначе одна безобидная строка спрятала бы настоящий отказ.
func allTolerated(msgs []string, tolerate func(string) bool) bool {
	if tolerate == nil {
		return false
	}
	for _, m := range msgs {
		if !tolerate(m) {
			return false
		}
	}
	return true
}

// ndmsStatusErrors recursively walks a decoded NDMS response and returns the
// messages of every object carrying status:"error" (case-insensitive),
// regardless of where it sits — scalar `"status":"error"` or a nested
// `"status":[{"status":"error",...}]` array. Returns nil on unparseable input
// (the transport layer already validated the top-level envelope).
func ndmsStatusErrors(resp []byte) []string {
	var root any
	if err := json.Unmarshal(resp, &root); err != nil {
		return nil
	}
	return walkNDMSStatusErrors(root)
}

func walkNDMSStatusErrors(v any) []string {
	switch t := v.(type) {
	case map[string]any:
		var msgs []string
		if s, ok := t["status"].(string); ok && strings.EqualFold(s, "error") {
			if m, ok := t["message"].(string); ok && m != "" {
				msgs = append(msgs, m)
			} else {
				msgs = append(msgs, "error")
			}
		}
		for _, val := range t {
			msgs = append(msgs, walkNDMSStatusErrors(val)...)
		}
		return msgs
	case []any:
		var msgs []string
		for _, val := range t {
			msgs = append(msgs, walkNDMSStatusErrors(val)...)
		}
		return msgs
	default:
		return nil
	}
}
