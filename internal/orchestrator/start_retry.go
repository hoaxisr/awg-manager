package orchestrator

import (
	"context"
	"fmt"
	"time"

	"github.com/hoaxisr/awg-manager/internal/ndms/query"
)

// Повтор холодного старта kernel-туннеля, упавшего на системном событии
// (бут, реконнект, WAN) — F625. На буте DNS роутера (127.0.0.1:53) отвечает
// не сразу, резолв endpoint'а падает, и без повтора туннель стоял до
// ручного старта. Пользовательский старт сюда не попадает: его отказ
// уходит в UI.
const (
	startRetryFirst = 15 * time.Second
	startRetryCap   = 5 * time.Minute
	// ~23 мин суммарно; дальше туннель поднимет следующее WAN-событие или
	// пользователь. Предел нужен и флешу: каждая попытка пишет .conf.
	startRetryMax = 8
)

func startRetryDelay(attempt int) time.Duration {
	d := startRetryFirst << (attempt - 1)
	if d > startRetryCap || d <= 0 {
		return startRetryCap
	}
	return d
}

// maybeStartRetry заводит цепочку повтора, если в группе был ColdStart
// kernel-туннеля, а туннель после неё не работает. Зовётся из executeGroup
// (только системные события) после группы или вместо неё, если замок
// туннеля взять не удалось.
func (o *Orchestrator) maybeStartRetry(group []Action) {
	tid := group[0].Tunnel
	hasColdStart := false
	for _, a := range group {
		if a.Type == ActionColdStartKernel {
			hasColdStart = true
			break
		}
	}
	if !hasColdStart {
		return
	}
	o.mu.Lock()
	usable := o.kernelModuleUsable
	o.mu.Unlock()
	// Вне o.mu: проверка зовёт lsmod.
	if usable != nil && !usable() {
		o.appLog.Info("start-retry", tid, "без повтора: модуль ядра amneziawg не загружается на этом ядре")
		return
	}
	o.mu.Lock()
	t := o.state.tunnels[tid]
	if t == nil || t.Running || o.startRetry[tid] {
		o.mu.Unlock()
		return
	}
	if o.startRetry == nil {
		o.startRetry = map[string]bool{}
	}
	o.startRetry[tid] = true
	o.mu.Unlock()
	o.scheduleStartRetry(tid, 1)
}

func (o *Orchestrator) scheduleStartRetry(tunnelID string, attempt int) {
	if attempt > startRetryMax {
		o.endStartRetry(tunnelID)
		o.appLog.Warn("start-retry", tunnelID,
			fmt.Sprintf("старт не удался после %d повторов — ждём WAN-событие или ручной старт", startRetryMax))
		return
	}
	d := startRetryDelay(attempt)
	o.appLog.Info("start-retry", tunnelID,
		fmt.Sprintf("повтор старта через %s (попытка %d/%d)", d, attempt, startRetryMax))
	o.scheduleFn(d, func() { o.retryStart(tunnelID, attempt) })
}

func (o *Orchestrator) endStartRetry(tunnelID string) {
	o.mu.Lock()
	delete(o.startRetry, tunnelID)
	o.mu.Unlock()
}

// retryStart — одна попытка. Решение принимается под замком туннеля: Stop
// пользователя, закончившийся раньше, уже записал Enabled=false, и
// повтор не поднимет туннель против его воли. Через HandleEvent нельзя:
// там decide идёт ДО замка, и старт, ждущий замка за чужим Stop, исполнился
// бы после него.
func (o *Orchestrator) retryStart(tunnelID string, attempt int) {
	o.mu.Lock()
	ctx := o.baseCtx
	o.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		o.endStartRetry(tunnelID)
		return
	}
	// Туннелем занят кто-то другой — он и решит его судьбу; итог прочтём
	// следующей попыткой. Занятость тратит попытку: залипший замок не
	// должен держать цепочку вечно.
	if !o.tryLockTunnel(tunnelID, "start-retry") {
		o.scheduleStartRetry(tunnelID, attempt+1)
		return
	}
	running := func() bool {
		defer o.unlockTunnel(tunnelID)
		return o.retryStartLocked(ctx, tunnelID)
	}()

	if running {
		o.endStartRetry(tunnelID)
		return
	}
	o.scheduleStartRetry(tunnelID, attempt+1)
}

// retryStartLocked возвращает true, если цепочку пора закончить: туннель
// работает, удалён или выключен.
func (o *Orchestrator) retryStartLocked(ctx context.Context, tunnelID string) bool {
	stored, err := o.store.Get(tunnelID)
	if err != nil || !stored.Enabled {
		return true
	}
	o.mu.Lock()
	o.state.ensureTunnel(tunnelID, o.store)
	actions := decideStart(Event{Type: EventStart, Tunnel: tunnelID}, &o.state)
	o.mu.Unlock()
	if len(actions) == 0 {
		return true // уже работает
	}
	_ = o.executeActions(query.WithActionList(ctx), actions)

	o.mu.Lock()
	defer o.mu.Unlock()
	t := o.state.tunnels[tunnelID]
	return t == nil || t.Running
}
