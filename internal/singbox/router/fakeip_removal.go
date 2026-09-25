package router

import (
	"context"

	"github.com/hoaxisr/awg-manager/internal/logging"
)

// ReleaseFakeIPTunForRemoval снимает интерфейс fakeip-tun при удалении пакета
// (`opkg remove` → `--cleanup`) — близнец ReleasePolicyTunForRemoval. Без него
// OpkgTun режима с адресом переживал удаление пакета (F450).
//
// Маршруты пула и CIDR отдельно не снимаются: NDMS удаляет их вместе с
// интерфейсом, к которому они привязаны (стенд 25.09.2026: v4-пул, CIDR и
// v6-пул ушли по `no interface`). Перехват DNS и ip rule таблицы 700 живут в
// ядре и снимаются в prerm — IPTables здесь не собираем: его конструктор
// переписывает netfilter-хуки, а на удалении нужно обратное.
//
// Персист fakeip после выключения режима очищается, поэтому запись есть
// только у включённого режима — выключенный интерфейса и не держит.
func ReleaseFakeIPTunForRemoval(ctx context.Context, d Deps) error {
	if d.Settings == nil || d.OpkgTun == nil {
		return nil
	}
	settings, err := d.Settings.Load()
	if err != nil {
		return err
	}
	st, ok := opkgTunOwned(settings, stateFakeIPTun)
	if !ok {
		return nil
	}
	s := &ServiceImpl{
		deps:   d,
		appLog: logging.NewScopedLogger(d.AppLog, logging.GroupRouting, logging.SubSingboxRouter),
	}
	ndmsName := tunNDMSName(st.Index)
	// Индекс из записи мог занять ЧУЖОЙ OpkgTun — см. ReleasePolicyTunForRemoval.
	if s.skipForeignTeardown(ctx, ndmsName, fakeIPTunDescription, "fakeip-remove") {
		return nil
	}
	return s.teardownOpkgTun(ctx, ndmsName, "fakeip-remove")
}
