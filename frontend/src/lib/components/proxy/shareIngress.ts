// Тумблер «Маршрутизация через sing-box» раздачи (RB-09). Состояние живёт в
// настройках роутера sing-box (`ingressInterfaces`), а НЕ в
// `ServerConfig.ingressEnabled` — то поле бэкенд не читает (спека §4.3).
//
// Ссылки набираются из kernel-имён интерфейсов сервера: это внутренние
// идентификаторы sing-box, на страницу они не попадают (правило имён ia §1.0 —
// человеку показываются NDMS-имена).

import type { WdttProcessStatus, WdttServerConfig } from '$lib/types';

/** Легаси-умолчания бэкенда: пустые имена = сервер вне NDMS (`internal/wdtt/types.go`). */
const LEGACY_WG_IFACE = 'wdtt0';
const LEGACY_RAW_IFACE = 'wdttraw0';

/**
 * Обе половины сервера: WireGuard-интерфейс и raw-интерфейс.
 *
 * Raw-имя берётся из статуса, но падает на то же, что считает бэкенд
 * (`kernelRawIface`): у остановленного сервера статуса может не быть, а имя
 * известно из конфига — легаси-умолчание тогда не соврёт.
 */
export function wdttIngressRefs(cfg: WdttServerConfig, status?: WdttProcessStatus): string[] {
	const wg = cfg.wgIface?.trim() || LEGACY_WG_IFACE;
	const raw = status?.rawIface?.trim() || cfg.rawIface?.trim() || LEGACY_RAW_IFACE;
	return [`iface:${wg}`, `iface:${raw}`];
}

/** Ingress включён, если хотя бы одна половина сервера заведена в sing-box. */
export function ingressOn(list: string[] | undefined, refs: string[]): boolean {
	const set = new Set(list ?? []);
	return refs.some((ref) => set.has(ref));
}

/**
 * Новый список интерфейсов для записи в настройки sing-box.
 *
 * Заодно снимаются мёртвые ссылки на собственные прежние имена: старая панель
 * писала `iface:wdttraw0` литералом, и после переезда половины в OpkgTun ссылка
 * на несуществующий интерфейс остаётся в настройках навсегда
 * (`pruneOrphanIngressRefs` чистит только `managed:`). Бэкендный близнец
 * (`staleWdttIngressRefs`, internal/wdtt/server_ingress.go) чистит их тем же
 * правилом, но только когда WG-ссылка уже на месте — выключенный ingress мимо
 * него проходит. wdtt-сервер в системе один (второй бэкенд не создаёт,
 * internal/wdtt/server.go:127), поэтому легаси-имена, кроме текущих, ничьи.
 */
export function nextIngressInterfaces(
	list: string[] | undefined,
	refs: string[],
	on: boolean,
): string[] {
	const set = new Set(list ?? []);
	for (const legacy of [`iface:${LEGACY_WG_IFACE}`, `iface:${LEGACY_RAW_IFACE}`]) {
		if (!refs.includes(legacy)) set.delete(legacy);
	}
	for (const ref of refs) {
		if (on) set.add(ref);
		else set.delete(ref);
	}
	return [...set];
}
