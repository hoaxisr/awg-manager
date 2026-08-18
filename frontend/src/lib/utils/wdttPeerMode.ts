import type { WdttClientConfig } from '$lib/types';

type ConnMode = 'wg' | 'raw';

function modeOf(c: WdttClientConfig): ConnMode {
	return c.connMode === 'raw' ? 'raw' : 'wg';
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
 * Переключение режима подставляет адрес из слота нового режима. Пустой слот
 * даёт пустое поле — лучше, чем молча уехать на порт соседнего режима.
 * Бэкенд подставить не может: он не отличает смену режима пользователем от
 * connMode, приехавшего в подписке.
 */
export function switchConnMode(c: WdttClientConfig, next: ConnMode): void {
	setPeer(c, c.peer);
	c.connMode = next;
	c.peer = (next === 'raw' ? c.peerRaw : c.peerWg)?.trim() ?? '';
}
