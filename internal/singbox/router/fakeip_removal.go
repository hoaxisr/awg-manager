package router

import (
	"context"
	"fmt"

	"github.com/hoaxisr/awg-manager/internal/logging"
)

// ReleaseFakeIPTunForRemoval снимает интерфейс fakeip-tun при удалении пакета
// (`opkg remove` → `--cleanup`) — близнец ReleasePolicyTunForRemoval. Без него
// OpkgTun режима с адресом переживал удаление пакета (F450).
//
// Маршруты пула и CIDR отдельно не снимаются: NDMS удаляет их вместе с
// интерфейсом, к которому они привязаны (стенд 25.09.2026: v4-пул, CIDR и
// v6-пул ушли по `no interface`). Перехват DNS и ip rule таблицы 700 живут в
// ядре и снимаются в prerm; перехват sb-router — ReleaseNetfilterForRemoval.
//
// Персист fakeip после выключения режима очищается, поэтому запись есть
// только у включённого режима — выключенный интерфейса и не держит.
func ReleaseFakeIPTunForRemoval(ctx context.Context, d Deps) error {
	if d.SwapGate == nil {
		return errNoSwapGate
	}
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
	// Сегменты возвращаем первыми, как в ReleasePolicyTunForRemoval: после
	// удаления пакета восстановить source-preserve будет неоткуда.
	if segs := natSegmentsOf(st); len(segs) > 0 {
		if e := s.restorePolicyTunNAT(ctx, segs); e != nil {
			s.appLog.Warn("fakeip-remove", ndmsName, "restore segment NAT: "+e.Error())
		}
	}
	// Индекс из записи мог занять ЧУЖОЙ OpkgTun — см. ReleasePolicyTunForRemoval.
	// Скан упал — отказ ошибкой (F493): `--cleanup` печатает её, интерфейс живёт.
	if proceed, err := s.teardownGate(ctx, ndmsName, "fakeip-remove", fakeIPTunDescription); !proceed {
		if err != nil {
			return fmt.Errorf("%s: %w", ndmsName, err)
		}
		return nil
	}
	return s.teardownOpkgTun(ctx, ndmsName, "fakeip-remove")
}
