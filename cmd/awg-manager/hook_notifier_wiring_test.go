package main

import (
	"testing"

	"github.com/hoaxisr/awg-manager/internal/tunnel"
)

type notifierSpy struct{ got tunnel.HookNotifier }

func (s *notifierSpy) SetHookNotifier(hn tunnel.HookNotifier) { s.got = hn }

type markerNotifier struct{ tunnel.HookNotifier }

// Оба оператора обязаны получить источник ожидаемых хуков. Пропущенный
// оператор не ломает ни сборку, ни тесты: его expectHook просто молчит, а
// собственное `conf: disabled` приезжает в оркестратор как внешнее событие.
func TestWireHookNotifiers_KernelOperatorNotForgotten(t *testing.T) {
	marker := &markerNotifier{}
	nwg, kernel := &notifierSpy{}, &notifierSpy{}

	wireHookNotifiers(marker, nwg, kernel)

	if nwg.got != tunnel.HookNotifier(marker) {
		t.Error("nwg-оператор остался без источника хуков")
	}
	if kernel.got != tunnel.HookNotifier(marker) {
		t.Error("kernel-оператор остался без источника хуков (OS5: expectHook станет no-op)")
	}
}

// Оператор, который хуки не принимает (не-OS5), проводку не ломает.
func TestWireHookNotifiers_ForeignOperatorIgnored(t *testing.T) {
	nwg := &notifierSpy{}
	wireHookNotifiers(&markerNotifier{}, nwg, struct{}{})
	if nwg.got == nil {
		t.Error("nwg-оператор остался без источника хуков")
	}
}
