package main

import (
	"context"
	"time"
)

// Бюджет остановки демона. SIGTERM (init-скрипт stop/restart, prerm на
// upgrade и remove — то есть и самообновление через opkg) и самоперезапуск
// (syscall.Exec) сохраняют конфигурацию роутера последним Flush и ждут конца
// записи — события ConfigurationSaved. Внешний ждущий не должен добить демона
// SIGKILL посреди записи: правки после последнего сохранения не доехали бы
// до startup-config (после сноса записи — сирота после ребута роутера).
const (
	// serveShutdownTimeout — потолок srv.Shutdown на SIGTERM (HTTP-соединения).
	serveShutdownTimeout = 10 * time.Second
	// shutdownFlushTimeout — ctx финального Flush: ожидание saveSem (летящий
	// fire), удержаний сноса, паузы после сноса и сам POST. Событие конца
	// записи Flush ждёт без ctx, не дольше ndmscommand.SaveEventCap.
	shutdownFlushTimeout = 20 * time.Second
	// serviceStopWait — сколько ждут выхода после SIGTERM init-скрипт
	// (STOP_WAIT) и `--service stop`. Обязан покрывать serveShutdownTimeout +
	// shutdownFlushTimeout + SaveEventCap с запасом на прочие хуки остановки
	// (TestShutdownBudget_FitsInitWait).
	serviceStopWait = 90 * time.Second
)

// flushSaveOnShutdown — последнее сохранение конфигурации роутера перед
// выходом: покрывает и отложенное сохранение после сноса записи (пауза
// SaveAfterRemoval, удержания: Flush ждёт их и шлёт POST —
// TestSave_FlushWaitsHoldsThenRemovalGap). Зовётся и на SIGTERM (onExit), и
// перед syscall.Exec (shutdown-хук) — пути взаимоисключающие. До L4
// финального ревью F595 SIGTERM шёл мимо: хуки исполняются только перед
// exec, и снятая перед остановкой запись оставалась в startup-config.
// Остаток: SIGKILL, паника, OOM — отложенное сохранение (debounce 3 с +
// пауза 5 с после сноса) теряется; снятые записи — сироты после ребута
// роутера до следующего любого сохранения.
func (a *app) flushSaveOnShutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownFlushTimeout)
	defer cancel()
	if err := a.ndmsSaveCoord.Flush(ctx); err != nil {
		a.bootLog.Warn("ndms-savecoord-flush", "shutdown", err.Error())
	}
}
