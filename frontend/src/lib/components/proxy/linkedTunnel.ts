// Связанный AWG-туннель клиента прокси.
//
// Бэкенд помнит связь полем `wdttClientId`/`freeTurnClientId`, но отдаёт его
// только в карточке одного туннеля (`GET /tunnels/get`); в списке
// (`/tunnels/all`, `internal/api/tunnels_view.go:listItems`) поля нет — это
// находка задачи 3. Пока поля в списке нет, туннель ищется по инварианту
// режима WG, который менеджер сам и держит: Endpoint туннеля равен
// локальному порту клиента (EX-10, `internal/api/wdtt.go:400-403` — при смене
// порта endpoint'ы связанных туннелей подтягиваются).

import type { TunnelListItem } from '$lib/types';

const LOOPBACK = new Set(['127.0.0.1', 'localhost', '::1', '[::1]']);

/** Порт из `host:port`; null, если порта нет. */
export function listenPort(listen?: string): string | null {
	const idx = listen?.lastIndexOf(':') ?? -1;
	if (idx < 0) return null;
	const port = listen!.slice(idx + 1).trim();
	return /^\d+$/.test(port) ? port : null;
}

/** Туннель, чей Endpoint смотрит в локальный порт клиента. */
export function findLinkedTunnel(
	tunnels: TunnelListItem[],
	listen?: string,
): TunnelListItem | null {
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
