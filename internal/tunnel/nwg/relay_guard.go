package nwg

// Релейная половина endpoint-стража: режим guardRelay следит не за
// peer.endpoint, а за target'ом обфускатора. Релей резолвит его имя ровно один
// раз при старте (resolve_host_wait в wg-obfuscator), поэтому смена A-записи
// доходит до него только перезапуском процесса, а host-route до прежнего
// адреса приходится переставлять.

import (
	"context"
	"fmt"
	"net"

	"github.com/hoaxisr/awg-manager/internal/storage"
)

// guardRegisterRelay ставит запись стража на target обфусцированного туннеля.
// Литеральный адрес резолвить нечего — запись снимается, чтобы страж не
// возил впустую.
func (o *OperatorNativeWG) guardRegisterRelay(stored *storage.AWGTunnel, ip string) {
	if stored.Obfuscator == nil {
		o.guardUnregister(stored.ID)
		return
	}
	host, port, err := net.SplitHostPort(stored.Obfuscator.Target)
	if err != nil || net.ParseIP(host) != nil {
		o.guardUnregister(stored.ID)
		return
	}
	names := NewNWGNames(stored.NWGIndex)
	o.guardRegister(stored.ID, guardEntry{
		iface:    names.IfaceName,
		pubkey:   stored.Peer.PublicKey,
		endpoint: net.JoinHostPort(ip, port),
		spec:     stored.Obfuscator.Target,
		name:     names.NDMSName,
		mode:     guardRelay,
	})
}

// syncRelayTarget доводит смену адреса target'а до релея: перезапуск процесса
// и перенос host-route.
func (o *OperatorNativeWG) syncRelayTarget(ctx context.Context, id string, e guardEntry, expected string) {
	// Адрес обновляем только на смену резолва: рестарт релея рвёт живую
	// сессию, вхолостую его гонять нельзя.
	if expected == e.endpoint || o.tunnelLookup == nil || o.obf == nil {
		return
	}
	// Под тем же per-tunnel замком, что и действия оркестратора: страж правит
	// host-route и состояние релея, то есть ровно то, что запрещено править
	// в обход замка владельцам в service.
	if o.tunnelLock == nil {
		o.restartRelayForNewTarget(ctx, id, e, expected)
		return
	}
	// Короткий дедлайн: страж вернётся через guardInterval, ждать освобождения
	// ему незачем, а держать проход (и Close демона) — тем более.
	lockCtx, cancel := context.WithTimeout(ctx, guardLockWait)
	defer cancel()
	if err := o.tunnelLock(lockCtx, id, "endpoint-guard", func() error {
		o.restartRelayForNewTarget(ctx, id, e, expected)
		return nil
	}); err != nil {
		o.appLog.Debug("endpoint-guard", e.name, "туннель занят, перенос адреса отложен: "+err.Error())
	}
}

// restartRelayForNewTarget — тело переноса, уже под замком.
func (o *OperatorNativeWG) restartRelayForNewTarget(ctx context.Context, id string, e guardEntry, expected string) {
	// Перепроверка под замком: Stop туннеля мог успеть снять запись, и тогда
	// рестарт поднял бы релей погашенного туннеля.
	if cur, ok := o.guardGet(id); !ok || cur.spec != e.spec || cur.mode != guardRelay ||
		cur.pubkey != e.pubkey || cur.iface != e.iface {
		return
	}
	stored, lookupErr := o.tunnelLookup(id)
	if lookupErr != nil || stored == nil || stored.Obfuscator == nil {
		o.appLog.Warn("endpoint-guard", e.name, "туннель не найден в хранилище — релей не перезапущен")
		return
	}
	freshIP, _, splitErr := net.SplitHostPort(expected)
	if splitErr != nil {
		return
	}
	// Stop обязателен: Runner.Start идемпотентен по СОДЕРЖИМОМУ INI, а там
	// имя, которое не менялось — без остановки он решит, что всё уже сделано,
	// и релей продолжит слать на прежний адрес.
	_ = o.obf.Stop(id)
	if err := o.obf.Start(ctx, id, stored.Obfuscator); err != nil {
		// Маршрут не трогаем: иначе каждая неудача стоила бы RCI-команды и
		// записи конфигурации роутера, а проход повторяется каждые 20 секунд.
		o.appLog.Warn("endpoint-guard", e.name, "перезапуск релея не удался: "+err.Error())
		return
	}
	// Маршрут — после успешного старта: до него релею всё равно нечем слать.
	prevIP := o.obfRouteIP(stored)
	o.trackEndpointIP(id, freshIP)
	o.moveObfHostRoute(ctx, stored, prevIP, freshIP)
	o.guardUpdateEndpoint(id, e.spec, expected, true)
	// Адрес обязан пережить рестарт демона: по нему снимается host-route, и
	// по нему же сосед с тем же target решает, чей это маршрут.
	if o.persistResolvedIP != nil {
		o.persistResolvedIP(id, freshIP)
	}
	o.appLog.Info("endpoint-guard", e.name,
		fmt.Sprintf("target %s сменил адрес на %s — релей перезапущен, host-route переставлен", e.spec, freshIP))
}
