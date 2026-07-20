import { describe, it, expect } from 'vitest';
import { buildOutboundOptions } from './outboundOptions';
import type { AWGTagInfo, SingboxRouterOutbound } from '$lib/types';

const awg: AWGTagInfo[] = [{ tag: 'awg-awg10', label: 'DE', kind: 'managed', iface: 'opkgtun10' }];
const composites: SingboxRouterOutbound[] = [
	{ type: 'urltest', tag: 'DE', outbounds: ['awg-awg10'], source: 'router' },
	{ type: 'selector', tag: 'other', outbounds: ['awg-awg10'], source: 'router' },
];

function values(groups: ReturnType<typeof buildOutboundOptions>): string[] {
	return groups.flatMap((g) => g.items.map((i) => i.value));
}

describe('buildOutboundOptions', () => {
	it('lists the composite among candidates without excludeTag', () => {
		const got = values(buildOutboundOptions(awg, null, composites, false));
		expect(got).toContain('DE');
	});

	it('excludes the edited tag so a composite cannot reference itself', () => {
		const got = values(buildOutboundOptions(awg, null, composites, false, null, 'DE'));
		expect(got).not.toContain('DE');
		// other outbounds remain selectable
		expect(got).toContain('awg-awg10');
		expect(got).toContain('other');
	});

	it('lists an awg3 tag with an empty iface without printing parens', () => {
		const awg3: AWGTagInfo[] = [{ tag: 'awg3-abc', label: 'RouteBox', kind: 'awg3', iface: '' }];
		const groups = buildOutboundOptions(awg3, null, null, false);
		const group = groups.find((g) => g.group === 'AWG3 туннели');
		expect(group).toBeDefined();
		expect(group?.items).toEqual([{ value: 'awg3-abc', label: 'RouteBox' }]);
	});

	it('drops a group that becomes empty after exclusion', () => {
		const onlySelf: SingboxRouterOutbound[] = [
			{ type: 'urltest', tag: 'DE', outbounds: ['awg-awg10'], source: 'router' },
		];
		const groups = buildOutboundOptions(null, null, onlySelf, false, null, 'DE');
		expect(groups.some((g) => g.group === 'Composite outbounds')).toBe(false);
	});
});
