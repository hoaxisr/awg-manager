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
 * The «что произойдёт» action list for a from→to transition: tears down the
 * source mode (teardownOf) then lists the destination mode's bring-up steps;
 * to==='off' is teardown-only (FE-spec §7.2 / §7.3).
 */
export function switchConsequences(from: RoutingMode, to: RoutingMode): string[] {
	const teardownOf = (mode: RoutingMode): string[] => {
		if (mode === 'fakeip-tun') {
			return ['Снятие fakeip: reject-маршрут на пул, дренаж соединений, снятие NDMS-маршрутов, остановка sing-box, удаление OpkgTun.'];
		}
		if (mode === 'tproxy') {
			return ['Снятие iptables TPROXY-цепочек и jump-правил.'];
		}
		return [];
	};

	if (to === 'fakeip-tun') {
		return [
			...teardownOf(from),
			'Перезапуск sing-box с tun-inbound.',
			'Создание/проверка интерфейса OpkgTun.',
			'DNS-перехват на туннеле (hijack-dns); адрес .2 для клиентов указывается вручную.',
			'Установка NDMS auto-маршрутов на пул fakeip.',
		];
	}
	if (to === 'tproxy') {
		return [
			...teardownOf(from),
			'Перезапуск sing-box.',
			'Поднятие iptables TPROXY-перехвата (jumps + AWGM-цепочки).',
		];
	}
	// to === 'off'
	const td = teardownOf(from);
	return td.length ? td : ['Маршрутизация выключена.'];
}
