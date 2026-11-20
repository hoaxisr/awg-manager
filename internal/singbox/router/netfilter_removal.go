package router

import (
	"context"

	"github.com/hoaxisr/awg-manager/internal/logging"
)

// ReleaseNetfilterForRemoval снимает перехват sb-router из ядра при удалении
// пакета (`opkg remove` → `--cleanup`), F623. Без него в режиме TPROXY в ядре
// оставались джампы PREROUTING в TPROXY/REDIRECT на порты, которые после
// остановки sing-box никто не слушает, плюс `ip rule` таблицы 100 и набор
// обхода: у устройств политики пропадал интернет до перезагрузки роутера.
// prerm снимает только файлы хуков, а сами правила в ядре — нет.
//
// Безусловно, без гейта по режиму: каждый шаг Uninstall идемпотентен, а в
// policy-tun в той же цепочке живут классы QoS (Install DSCPOnly). Тот же
// порядок, что в disable: набор — только после снятия правил с `--match-set`.
//
// Перехват IPv6 снимается так же и по той же причине: его джампы в ip6tables,
// правило `ip -6 rule` и набор AWGM-BYPASS6 остановку sing-box переживают
// точно так же, а хук 53 prerm удаляет вместе с остальными. Тоже безусловно:
// на роутере без ip6tables шаги просто не находят, что снимать.
//
// ServiceImpl собирается мимо NewService по той же причине, что в
// ReleasePolicyTunForRemoval: тот переписывает netfilter-хук на диске.
func ReleaseNetfilterForRemoval(ctx context.Context, d Deps) {
	if d.IPTables == nil {
		d.IPTables = NewIPTables()
	}
	s := &ServiceImpl{
		deps: d,
		// appLog — для sweepV6: без него отказ сноса AWGM-BYPASS6 терялся бы молча.
		appLog:    logging.NewScopedLogger(d.AppLog, logging.GroupRouting, logging.SubSingboxRouter),
		bypassLog: logging.NewScopedLogger(d.AppLog, logging.GroupRouting, logging.SubBypassSet),
	}
	s.deps.IPTables.Uninstall(ctx)
	s.deps.IPTables.UninstallCloudOutput(ctx)
	removalTeardownBypassSet(s, ctx)
	// sweepV6 сам держит порядок «правила, потом набор».
	s.sweepV6(ctx)
}

// removalTeardownBypassSet — шов теста: без него тест экспортируемой функции
// звал бы ipset хоста.
var removalTeardownBypassSet = (*ServiceImpl).teardownBypassSet
