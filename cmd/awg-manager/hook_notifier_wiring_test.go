package main

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

type notifierSpy struct{ got tunnel.HookNotifier }

func (s *notifierSpy) SetHookNotifier(hn tunnel.HookNotifier) { s.got = hn }

type markerNotifier struct{ tunnel.HookNotifier }

// nwg-оператор обязан получить источник ожидаемых хуков. Пропущенный
// оператор не ломает ни сборку, ни тесты: его expectHook просто молчит, а
// собственное `conf: disabled` приезжает в оркестратор как внешнее событие.
func TestWireHookNotifiers_NWGOperatorNotForgotten(t *testing.T) {
	marker := &markerNotifier{}
	nwg := &notifierSpy{}

	wireHookNotifiers(marker, nwg)

	if nwg.got != tunnel.HookNotifier(marker) {
		t.Error("nwg-оператор остался без источника хуков")
	}
}
