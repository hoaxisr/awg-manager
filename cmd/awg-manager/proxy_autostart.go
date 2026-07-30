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
// падает на lookup login.vk.ru, если резолвер ещё не жив.
func (a *app) scheduleProxyClientAutostart(trigger string) {
	go func() {
		const dnsSettle = 8 * time.Second
		select {
		case <-a.shutdownCtx.Done():
			return
		case <-time.After(dnsSettle):
		}
		a.resumeEnabledProxyClients(trigger)
		// Повтор для enabled-клиентов, не переживших первую попытку (DNS race).
		select {
		case <-a.shutdownCtx.Done():
			return
		case <-time.After(30 * time.Second):
		}
		a.resumeEnabledProxyClients(trigger + "-retry")
		if n, err := backup.ReconcileLinkedEndpoints(a.dataDir); err != nil && a.bootLog != nil {
			a.bootLog.Warn("startup", "", "reconcile linked endpoints: "+err.Error())
		} else if n > 0 && a.bootLog != nil {
			a.bootLog.Info("startup", "", fmt.Sprintf("synced %d linked tunnel endpoint(s) (%s)", n, trigger))
		}
	}()
}
