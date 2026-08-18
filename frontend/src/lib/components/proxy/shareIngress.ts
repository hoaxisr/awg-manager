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

/** Обе половины сервера: WireGuard-интерфейс и raw-интерфейс. */
export function wdttIngressRefs(cfg: WdttServerConfig, status?: WdttProcessStatus): string[] {
	const wg = cfg.wgIface?.trim() || LEGACY_WG_IFACE;
	const raw = status?.rawIface?.trim() || LEGACY_RAW_IFACE;
	return [`iface:${wg}`, `iface:${raw}`];
}

/** Ingress включён, если хотя бы одна половина сервера заведена в sing-box. */
export function ingressOn(list: string[] | undefined, refs: string[]): boolean {
	const set = new Set(list ?? []);
	return refs.some((ref) => set.has(ref));
}

/** Новый список интерфейсов для записи в настройки sing-box. */
export function nextIngressInterfaces(
	list: string[] | undefined,
	refs: string[],
	on: boolean,
): string[] {
	const set = new Set(list ?? []);
	for (const ref of refs) {
		if (on) set.add(ref);
		else set.delete(ref);
	}
	return [...set];
}
