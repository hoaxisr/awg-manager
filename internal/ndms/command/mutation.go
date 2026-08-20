package command

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// postMutation is the common post-then-invalidate pattern shared by most
// command methods: POST a single payload, wrap a transport error with
// opDesc, Request a save on success, then run each cache invalidator.
//
// opDesc is used as an error prefix ("opDesc: <err>") — short present-tense
// phrasing such as "create policy foo" reads best in logs.
//
// Invalidators are plain closures so callers can mix per-key
// (`c.queries.Interfaces.Invalidate(name)`) and whole-store
// (`c.queries.RunningConfig.InvalidateAll`) cache drops in the same call.
// InvalidateAll without parens works as a method value.
func postMutation(
	ctx context.Context,
	poster Poster,
	save *SaveCoordinator,
	payload any,
	opDesc string,
	invalidators ...func(),
) error {
	if _, err := poster.Post(ctx, payload); err != nil {
		return fmt.Errorf("%s: %w", opDesc, err)
	}
	save.Request()
	for _, inv := range invalidators {
		inv()
	}
	return nil
}

// postMutationChecked is postMutation plus a nested-status check. Some NDMS
// mutations (notably interface.<name>.wireguard.peer ops) answer HTTP 200 with
// a benign top-level envelope while reporting the real failure inside a nested
// status[] array — which the transport-level error check does not inspect, so
// postMutation would treat the call as success. This variant scans the
// response for any explicit status:"error" entry and fails closed when found,
// without requesting a save or invalidating caches.
//
// It is deliberately additive: a normal success response contains no
// status:"error" entry, so behaviour is unchanged for those. NOTE: the exact
// nested shape for peer ops is modelled on the import path and should be
// confirmed against a live router (e.g. a deliberately-invalid peer add)
// before relying on it as the sole guard.
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
	resp, err := poster.Post(ctx, payload)
	if err != nil {
		return fmt.Errorf("%s: %w", opDesc, err)
	}
	if msgs := ndmsStatusErrors(resp); len(msgs) > 0 && !allTolerated(msgs, tolerate) {
		return fmt.Errorf("%s: router reported error: %s", opDesc, strings.Join(msgs, "; "))
	}
	save.Request()
	for _, inv := range invalidators {
		inv()
	}
	return nil
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

// isMissingInterface распознаёт формулировку NDMS «такого интерфейса нет»
// (снято на роутере: `unable to find interface "OpkgTun9"`). Формулировка
// отказа в СОЗДАНИИ другая — `unable to find X in "Network::Interface::Base"`,
// — и под этот предикат намеренно не попадает.
func isMissingInterface(msg string) bool {
	return strings.Contains(strings.ToLower(msg), "unable to find interface")
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
