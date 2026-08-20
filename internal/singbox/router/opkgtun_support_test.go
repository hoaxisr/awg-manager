package router

import (
	"context"
	"strings"
	"testing"
)

// На прошивке без OpkgTun переключение в tun-режим обязано отказать СРАЗУ,
// не разбирая текущий режим: иначе пользователь остаётся без маршрутизации
// вообще (в #768 спасал только откат).
func TestSwitch_TunModesRejectedOnOS4(t *testing.T) {
	for _, target := range []string{stateFakeIPTun, statePolicyTun} {
		t.Run(target, func(t *testing.T) {
			h := newTransitionHarness(t)
			h.svc.deps.FirmwareRelease = func() string { return "4.03.C.8.0-0" }
			h.seedState(t, stateTProxy, true)

			err := h.svc.SwitchRoutingMode(context.Background(), target)
			if err == nil {
				t.Fatal("ожидался отказ на прошивке без OpkgTun")
			}
			if !strings.Contains(err.Error(), "OpkgTun") {
				t.Errorf("сообщение не называет причину: %v", err)
			}
			if mode, enabled := h.state(t); mode != stateTProxy || !enabled {
				t.Errorf("текущий режим разобран: mode=%q enabled=%v", mode, enabled)
			}
		})
	}
}

// tproxy и off OpkgTun не требуют — на той же прошивке они работают.
func TestSwitch_TProxyAllowedOnOS4(t *testing.T) {
	h := newTransitionHarness(t)
	h.svc.deps.FirmwareRelease = func() string { return "4.03.C.8.0-0" }
	h.seedState(t, stateOff, false)

	if err := h.svc.SwitchRoutingMode(context.Background(), stateTProxy); err != nil {
		t.Fatalf("tproxy на OS4 должен включаться: %v", err)
	}
	if mode, enabled := h.state(t); mode != stateTProxy || !enabled {
		t.Errorf("режим = %q enabled=%v, ожидался включённый tproxy", mode, enabled)
	}
}
