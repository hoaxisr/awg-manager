import { describe, expect, it } from 'vitest';
import type { RoutingTunnel } from '$lib/types';
import { hrTargetTunnel } from './hrTargetTunnel';

const tunnels: RoutingTunnel[] = [
	{ id: 'awg10', name: 'Мой', iface: 'nwg0', type: 'managed', status: 'running', available: true },
	{ id: 'system:Wireguard1', name: 'Офис', iface: 'Wireguard1', type: 'system', status: 'up', available: true },
];

describe('hrTargetTunnel (F498)', () => {
	it('managed: по имени ядра', () => {
		expect(hrTargetTunnel(tunnels, 'nwg0', 'nwg0')?.id).toBe('awg10');
	});

	it('system: по tunnelId, в файле имя ядра', () => {
		expect(hrTargetTunnel(tunnels, 'nwg1', 'system:Wireguard1')?.id).toBe('system:Wireguard1');
	});

	it('неизвестная цель не находится (цель сломана)', () => {
		expect(hrTargetTunnel(tunnels, 'nwg7', 'nwg7')).toBeUndefined();
	});

	it('имя туннеля с именем ядра не сравнивается', () => {
		expect(hrTargetTunnel(tunnels, 'Мой', 'Мой')).toBeUndefined();
	});
});
