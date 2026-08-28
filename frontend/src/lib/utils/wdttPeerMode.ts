import type { WdttClientConfig } from '$lib/types';

type ConnMode = 'wg' | 'raw';

/**
 * Смещение raw-порта сервера относительно DTLS: конвенция qWDTT 1.4. Сервер
 * поднимает `-listen-raw` на DTLS+1, когда явный адрес не задан — то же
 * правило живёт в `roles.WdttServerConfig.EffectiveRawListen` (config.go) и в
 * генераторе ссылок (`LinkListenPortForMode`, ports.go).
 */
const RAW_PORT_OFFSET = 1;

function modeOf(c: WdttClientConfig): ConnMode {
	return c.connMode === 'raw' ? 'raw' : 'wg';
}

/** Разбор `host:port` с учётом формы `[::1]:56002`. */
function splitPeer(addr: string): { host: string; port: number } | null {
	const value = addr.trim();
	if (!value) return null;
	const idx = value.startsWith('[') ? value.indexOf(']:') + 1 : value.lastIndexOf(':');
	if (idx <= 0) return null;
	const port = Number(value.slice(idx + 1));
	if (!Number.isInteger(port) || port <= 0 || port > 65535) return null;
	return { host: value.slice(0, idx), port };
}

/**
 * Адрес соседнего режима по конвенции портов: raw = DTLS+1, обратно −1.
 *
 * Нужен потому, что у клиента ОДИН `-peer`, а сервер слушает режимы на разных
 * портах: пользователь, переключивший режим, иначе обязан знать и вводить
 * второй адрес руками. В ссылке `wdtt://` лежит только DTLS-порт, поэтому у
 * всех, кто импортировал профиль, raw-слот пуст — и первое же переключение
 * давало мёртвый инстанс (стенд 2026-08-28).
 *
 * Пусто, если исходный адрес пуст или без порта: выдумывать хост не из чего.
 */
export function derivePeerForMode(from: string, next: ConnMode): string {
	const parsed = splitPeer(from);
	if (!parsed) return '';
	const port = parsed.port + (next === 'raw' ? RAW_PORT_OFFSET : -RAW_PORT_OFFSET);
	if (port <= 0 || port > 65535) return '';
	return `${parsed.host}:${port}`;
}

/**
 * У raw и wg разные порты сервера, поэтому адрес каждого режима живёт в своём
 * слоте (peerWg/peerRaw). Инвариант «peer = слот активного режима» держит
 * бэкенд (wdtt.normalizePeers), причём peer у него главнее слота — значит
 * редактирование активного слота обязано писать и в peer, иначе правку
 * затрёт при сохранении.
 */
export function setPeer(c: WdttClientConfig, value: string): void {
	c.peer = value;
	if (modeOf(c) === 'raw') c.peerRaw = value;
	else c.peerWg = value;
}

/**
 * Переключение режима подставляет адрес из слота нового режима, а если слот
 * пуст — выводит его из адреса прежнего режима по конвенции портов. Введённый
 * руками адрес имеет приоритет над вычисленным: слот не перетирается.
 */
export function switchConnMode(c: WdttClientConfig, next: ConnMode): void {
	setPeer(c, c.peer);
	const previous = c.peer;
	c.connMode = next;
	const slot = (next === 'raw' ? c.peerRaw : c.peerWg)?.trim() ?? '';
	c.peer = slot || derivePeerForMode(previous, next);
}
