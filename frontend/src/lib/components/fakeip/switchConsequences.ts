// Pure, direction-aware copy for the FakeIP mode-switch confirmation
// (FE-spec §7.2 / §7.3 / §12.4). Kept side-effect-free so the wording can be
// unit-tested without mounting the Svelte component.

export type RoutingMode = 'off' | 'tproxy' | 'fakeip-tun';

/** Russian display label for a routing mode (no emoji per house rules). */
export function humanLabel(mode: RoutingMode): string {
	switch (mode) {
		case 'off':
			return 'Выключен';
		case 'tproxy':
			return 'TPROXY';
		case 'fakeip-tun':
			return 'FakeIP';
	}
}

/**
 * The «что произойдёт» action list for a from→to transition. Direction-aware:
 * enabling fakeip-tun lists the bring-up steps; switching out of fakeip-tun
 * lists the tear-down + anti-leak steps (FE-spec §7.2 / §7.3).
 */
export function switchConsequences(from: RoutingMode, to: RoutingMode): string[] {
	if (to === 'fakeip-tun') {
		const items = [
			'Перезапуск sing-box с tun-inbound.',
			'Создание/проверка интерфейса OpkgTun.',
		];
		if (from === 'tproxy') {
			items.push('Снятие iptables TPROXY-цепочек и jump-правил.');
		}
		items.push('Смена доставки DNS: перехват :53 → DHCP-выдача адреса .2 по сегментам.');
		items.push('Установка NDMS auto-маршрутов на пул fakeip.');
		return items;
	}

	if (from === 'fakeip-tun') {
		const items = [
			'Reject-маршрут на пул fakeip (анти-утечка трафика).',
			'Возврат DHCP DNS-сервера на роутер.',
			'Дренаж активных соединений.',
			'Снятие NDMS auto-маршрутов.',
			'Остановка sing-box.',
			'Удаление интерфейса OpkgTun.',
		];
		if (to === 'tproxy') {
			items.push('Поднятие TPROXY-перехвата.');
		}
		return items;
	}

	// Transitions not involving fakeip-tun are out of this screen's scope.
	return [];
}
