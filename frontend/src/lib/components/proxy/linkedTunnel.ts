// Связанный AWG-туннель клиента прокси.
//
// Связь помнит бэкенд полем `wdttClientId`, и с коммита «api: отдавать
// wdttClientId в списке туннелей» оно приходит в списке
// (`internal/api/tunnels_view.go`, `TunnelListItemDTO.WdttClientID`) — это и
// есть источник правды.
//
// Endpoint-эвристика оставлена фоллбэком: у туннелей, созданных до появления
// поля, оно пустое, а у FreeTurn-клиента в списке нет и самого поля (бэкенд
// отдаёт `freeTurnClientId` только в карточке туннеля). Инвариант держит сам
// менеджер: Endpoint туннеля равен локальному порту клиента (EX-10,
// `internal/api/wdtt.go:400-403` — при смене порта endpoint'ы связанных
// туннелей подтягиваются).

import type { TunnelListItem } from '$lib/types';

const LOOPBACK = new Set(['127.0.0.1', 'localhost', '::1', '[::1]']);

/** Порт из `host:port`; null, если порта нет. */
export function listenPort(listen?: string): string | null {
	const idx = listen?.lastIndexOf(':') ?? -1;
	if (idx < 0) return null;
	const port = listen!.slice(idx + 1).trim();
	return /^\d+$/.test(port) ? port : null;
}

/** Туннель клиента: по `wdttClientId`, иначе по локальному порту. */
export function findLinkedTunnel(
	tunnels: TunnelListItem[],
	listen?: string,
	wdttClientId?: string,
): TunnelListItem | null {
	if (wdttClientId) {
		const linked = tunnels.find((t) => t.wdttClientId === wdttClientId);
		if (linked) return linked;
	}
	const port = listenPort(listen);
	if (!port) return null;
	for (const t of tunnels) {
		const ep = t.endpoint?.trim();
		if (!ep) continue;
		const idx = ep.lastIndexOf(':');
		if (idx < 0) continue;
		if (ep.slice(idx + 1).trim() !== port) continue;
		if (LOOPBACK.has(ep.slice(0, idx).trim())) return t;
	}
	return null;
}
