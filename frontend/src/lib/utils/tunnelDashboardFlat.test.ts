import { describe, it, expect } from 'vitest';
import { buildFlatDashboardItems } from './tunnelDashboardFlat';

describe('buildFlatDashboardItems', () => {
	it('keeps category order and sorts alphabetically within each group', () => {
		const items = buildFlatDashboardItems({
			awg: [{ id: 'z-tunnel', name: 'Zulu AWG' } as never],
			system: [{ id: 'sys-1', interfaceName: 'wg-sys', description: 'Mike system' } as never],
			external: [{ interfaceName: 'ext-alpha' } as never],
			awg3: [{ id: 'awg3-x', tag: 'Charlie AWG3', host: 'h:1' } as never],
			singbox: [{ tag: 'Bravo SB' } as never],
			subscriptionsActive: [
				{
					subscription: { id: 'sub-a', label: 'Alpha sub' },
					activeMember: { tag: 'm1' },
				} as never,
			],
			subscriptionsStopped: [{ id: 'sub-off', label: 'Stopped sub' } as never],
		});

		expect(items.map((item) => item.kind)).toEqual([
			'awg-managed',
			'awg-system',
			'awg-external',
			'awg3',
			'singbox',
			'sub-active',
			'sub-stopped',
		]);
		expect(items.map((item) => item.name)).toEqual([
			'Zulu AWG',
			'Mike system',
			'ext-alpha',
			'Charlie AWG3',
			'Bravo SB',
			'Alpha sub',
			'Stopped sub',
		]);
	});

	it('does not interleave categories by name', () => {
		const items = buildFlatDashboardItems({
			awg: [{ id: 'a', name: 'Zulu' } as never],
			system: [],
			external: [],
			awg3: [],
			singbox: [{ tag: 'Alpha SB' } as never],
			subscriptionsActive: [],
			subscriptionsStopped: [],
		});

		expect(items.map((item) => item.kind)).toEqual(['awg-managed', 'singbox']);
		expect(items[0].name).toBe('Zulu');
		expect(items[1].name).toBe('Alpha SB');
	});

	it('gives active and stopped subscriptions the same identity key', () => {
		const active = buildFlatDashboardItems({
			awg: [],
			system: [],
			external: [],
			awg3: [],
			singbox: [],
			subscriptionsActive: [
				{
					subscription: { id: 'sub-1', label: 'My sub' },
					activeMember: { tag: 'm1' },
				} as never,
			],
			subscriptionsStopped: [],
		});
		const stopped = buildFlatDashboardItems({
			awg: [],
			system: [],
			external: [],
			awg3: [],
			singbox: [],
			subscriptionsActive: [],
			subscriptionsStopped: [{ id: 'sub-1', label: 'My sub' } as never],
		});

		expect(active[0].kind).toBe('sub-active');
		expect(stopped[0].kind).toBe('sub-stopped');
		expect(active[0].key).toBe('sub:sub-1');
		expect(stopped[0].key).toBe('sub:sub-1');
	});
});
