package router

import (
	"context"

	"github.com/hoaxisr/awg-manager/internal/logging"
)

// ReleasePolicyTunForRemoval снимает интерфейс policy-tun при удалении пакета
// (`opkg remove` → `--cleanup`): вернуть NAT сегментов → снять NDMS-дефолт →
// снести интерфейс. Это порядок выключения режима без слота и ingress, а не
// персист-реапа: тот дефолт вообще не снимает, ему хватает исчезновения
// интерфейса вместе с маршрутом.
//
// Снятие идёт по ПЕРСИСТУ и НЕ смотрит на Provisioned: выключенный режим хранит
// именно {Provisioned:false, Index}, а интерфейс при этом жив (его удерживает
// holdOpkgTun). Гейт по Provisioned оставил бы OpkgTun на роутере после
// удаления пакета.
//
// Почему в Go, а не в prerm: разобрать settings.json на прошивке нечем — jq
// там нет (единая запись владения пишет `index` всегда, но это не помогает).
// Сам prerm трогать не нужно: он уже зовёт `--cleanup`, а на upgrade
// только останавливает демона, поэтому интерфейс переживает обновление пакета.
//
// Собирает ServiceImpl напрямую, а не через NewService: тот идемпотентно
// переписывает netfilter-хук на диске, а на пути удаления пакета возвращать
// файлы — ровно обратное тому, что требуется.
func ReleasePolicyTunForRemoval(ctx context.Context, d Deps) error {
	if d.Settings == nil || d.OpkgTun == nil {
		return nil
	}
	settings, err := d.Settings.Load()
	if err != nil {
		return err
	}
	st, ok := opkgTunOwned(settings, statePolicyTun)
	if !ok {
		return nil
	}
	s := &ServiceImpl{
		deps:   d,
		appLog: logging.NewScopedLogger(d.AppLog, logging.GroupRouting, logging.SubSingboxRouter),
	}
	ndmsName := fakeIPNDMSName(st.Index)

	// Сегменты возвращаем ПЕРВЫМИ, пока дефолт ещё на tun: иначе удаление
	// пакета при включённом source-preserve оставило бы их на static-NAT
	// навсегда — восстановить эту запись после удаления будет уже неоткуда.
	if segs := natSegmentsOf(st); len(segs) > 0 {
		if e := s.restorePolicyTunNAT(ctx, segs); e != nil {
			s.appLog.Warn("policy-tun-remove", ndmsName, "restore segment NAT: "+e.Error())
		}
	}

	// Дефолт снимаем до сноса интерфейса: переживший маршрут остался бы в
	// конфигурации на несуществующем имени, а fakeip позже может занять тот же
	// номер — и чужой дефолт ожил бы на его интерфейсе.
	if d.DefaultRoute != nil {
		if e := d.DefaultRoute.RemoveDefaultRoute(ctx, ndmsName); e != nil {
			s.appLog.Warn("policy-tun-remove", ndmsName, "remove default route: "+e.Error())
		}
		if e := d.DefaultRoute.RemoveIPv6DefaultRoute(ctx, ndmsName); e != nil {
			s.appLog.Warn("policy-tun-remove", ndmsName, "remove ipv6 default route: "+e.Error())
		}
	}

	// teardownOpkgTun, а не holdOpkgTun: удержание существует ради permit'а в
	// политике, а вместе с пакетом уходит и он.
	return s.teardownOpkgTun(ctx, ndmsName, "policy-tun-remove")
}
