package main

import (
	"context"
	"fmt"
	"time"

	"github.com/hoaxisr/awg-manager/internal/events"
)

// ingressRuntimeWatchInterval — как часто сторож сверяет рантайм ingress.
// Это замена 5-секундного опроса карточек серверов из КАЖДОЙ открытой
// вкладки: здесь один дешёвый in-process снимок, а наружу уходит подсказка
// только на смену.
const ingressRuntimeWatchInterval = 10 * time.Second

// ingressRuntimeDigest — то, что карточки VLESS/Telegram/диспетчера
// показывают о рантайме и что меняется без запроса к панели: падение
// процесса, живость воркеров Telegram, здоровье его выходов. Конфиг сюда не
// входит — его правки публикуют хендлеры и onReload сервисов.
// vlessserver.GetStatus не берём: он запускает бинарь ради версии.
func (a *app) ingressRuntimeDigest() string {
	var vless, disp bool
	if a.vlessServerService != nil {
		vless = a.vlessServerService.IsRunning()
	}
	if a.cdnDispatcher != nil {
		disp = a.cdnDispatcher.IsRunning()
	}
	tg := ""
	if a.tgWebProxyService != nil {
		st := a.tgWebProxyService.GetStatus()
		tg = fmt.Sprintf("%t/%t/%t/%t/%s/%t", st.Running, st.DirectOnline, st.RawOnline,
			st.WebProxyOnline, st.UpstreamStatus, st.RecoveryRequired)
	}
	return fmt.Sprintf("vless=%t disp=%t tg=%s", vless, disp, tg)
}

// startIngressRuntimeWatch публикует ResourceServersIngress, когда рантайм
// ingress сменился сам по себе.
func (a *app) startIngressRuntimeWatch(ctx context.Context) {
	go watchDigestChanges(ctx, ingressRuntimeWatchInterval, a.ingressRuntimeDigest, func() {
		a.eventBus.PublishInvalidated(events.ResourceServersIngress, "runtime-changed")
	})
}

// watchDigestChanges снимает sample раз в interval и зовёт onChange только
// на смену значения. Первый снимок — точка отсчёта, не смена.
func watchDigestChanges(ctx context.Context, interval time.Duration, sample func() string, onChange func()) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	prev := sample()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cur := sample()
			if cur != prev {
				prev = cur
				onChange()
			}
		}
	}
}
