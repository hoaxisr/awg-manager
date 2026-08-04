package main

import (
	"fmt"
	"time"

	"github.com/hoaxisr/awg-manager/internal/backup"
)

// resumeEnabledProxyClients поднимает FreeTurn/WDTT с Enabled==true.
// Идемпотентно: повторный вызов безопасен (живые процессы не трогаются).
func (a *app) resumeEnabledProxyClients(reason string) {
	if a.freeturnService == nil || a.wdttService == nil {
		return
	}
	if backup.HasPostRestoreMarker(a.dataDir) {
		return
	}
	a.bootLog.Info("startup", "", "proxy autostart: "+reason)
	go a.freeturnService.ResumeEnabled()
	go a.wdttService.ResumeEnabled()
}

// scheduleProxyClientAutostart откладывает автостарт до готовности WAN/NDMS и
// даёт DNS-стеку (sing-box на 127.0.0.1:53) время подняться — WDTT vkcalls
// падает на lookup login.vk.ru, если резолвер ещё не жив. Серверы FT/WDTT
// могут ждать backend WG — повторные попытки через 30s и 2m.
func (a *app) scheduleProxyClientAutostart(trigger string) {
	go func() {
		const dnsSettle = 8 * time.Second
		select {
		case <-a.shutdownCtx.Done():
			return
		case <-time.After(dnsSettle):
		}
		a.resumeEnabledProxyClients(trigger)
		select {
		case <-a.shutdownCtx.Done():
			return
		case <-time.After(30 * time.Second):
		}
		a.resumeEnabledProxyClients(trigger + "-retry")
		select {
		case <-a.shutdownCtx.Done():
			return
		case <-time.After(2 * time.Minute):
		}
		a.resumeEnabledProxyClients(trigger + "-retry2")
	}()
}

// reconcileLinkedEndpoints синхронизирует Endpoint linked-туннелей с listen
// прокси-клиентов. Один вызов на бут: listen назначается при создании клиента,
// а не при старте процесса, поэтому повторять после автостарта незачем.
func (a *app) reconcileLinkedEndpoints(scope string) {
	n, err := backup.ReconcileLinkedEndpoints(a.dataDir, a.awgStore)
	if a.bootLog == nil {
		return
	}
	if err != nil {
		a.bootLog.Warn(scope, "", "reconcile linked endpoints: "+err.Error())
		return
	}
	if n > 0 {
		a.bootLog.Info(scope, "", fmt.Sprintf("synced %d linked tunnel endpoint(s)", n))
	}
}
