import { describe, it, expect } from 'vitest';
import { resolveMemberLabel } from './memberLabel';
import type { Subscription } from '$lib/types';
import type { OutboundGroup } from '$lib/components/routing/singboxRouter/outboundOptions';

const subs = [
	{
		selectorTag: 'sub-58dea6ef-c0ca6dbc',
		label: 'Veesp LV',
		members: [
			{ tag: 'sub-58dea6ef-node3', label: 'LV node 3', server: '1.2.3.4' },
			{ tag: 'sub-58dea6ef-node4', server: '5.6.7.8' },
		],
	},
	{ selectorTag: 'sub-empty', label: '', members: [] },
] as unknown as Subscription[];

const options: OutboundGroup[] = [
	{ group: 'Специальные', items: [{ value: 'direct', label: 'direct (мимо VPN)' }] },
	{ group: 'AWG туннели', items: [{ value: 'awg-awg10', label: 'Office VPN (t2s10)' }] },
];

describe('resolveMemberLabel', () => {
	it('тег-селектор подписки → чистое имя подписки', () => {
		expect(resolveMemberLabel('sub-58dea6ef-c0ca6dbc', subs, options)).toBe('Veesp LV');
	});

	it('селектор с пустым label → сырой тег (fallback)', () => {
		expect(resolveMemberLabel('sub-empty', subs, options)).toBe('sub-empty');
	});

	it('тег члена с label → label', () => {
		expect(resolveMemberLabel('sub-58dea6ef-node3', subs, options)).toBe('LV node 3');
	});

	it('тег члена без label → server', () => {
		expect(resolveMemberLabel('sub-58dea6ef-node4', subs, options)).toBe('5.6.7.8');
	});

	it('AWG туннель → label из outboundOptions', () => {
		expect(resolveMemberLabel('awg-awg10', subs, options)).toBe('Office VPN (t2s10)');
	});

	it('direct → label из outboundOptions', () => {
		expect(resolveMemberLabel('direct', subs, options)).toBe('direct (мимо VPN)');
	});

	it('неизвестный тег → сырой тег (fallback)', () => {
		expect(resolveMemberLabel('mystery', subs, options)).toBe('mystery');
	});

	it('подписки null — туннель резолвится по outboundOptions', () => {
		expect(resolveMemberLabel('awg-awg10', null, options)).toBe('Office VPN (t2s10)');
	});

	it('пустой тег → пустой тег', () => {
		expect(resolveMemberLabel('', subs, options)).toBe('');
	});
});
