import { describe, expect, it } from 'vitest';
import { bindInterfaceLabel, directBindChoices } from './bindInterface';

const base = { name: 'csqtt0', id: '', label: 'csqtt0', up: true, priority: 0 };

describe('bindInterfaceLabel', () => {
	it('up interface — label and name only', () => {
		expect(bindInterfaceLabel({ ...base, name: 'ppp0', label: 'PPPoE' })).toBe('PPPoE · ppp0');
	});
	it('native down keeps (down)', () => {
		expect(bindInterfaceLabel({ ...base, name: 'ppp0', label: 'PPPoE', up: false })).toBe('PPPoE · ppp0 (down)');
	});
	it('foreign without carrier', () => {
		expect(bindInterfaceLabel({ ...base, foreign: true, up: false })).toBe('csqtt0 · csqtt0 (нет несущей)');
	});
	it('foreign absent', () => {
		expect(bindInterfaceLabel({ ...base, foreign: true, up: false, absent: true })).toBe('csqtt0 · csqtt0 (нет в системе)');
	});
});

describe('directBindChoices', () => {
	const list = [
		{ ...base, name: 'nocli0', label: 'OpenConnect' },
		{ ...base, name: 'ppp0', label: 'PPPoE' },
		{ ...base, name: 'ipsec0', label: 'IPSec' }
	];
	const test = { type: 'direct' as const, tag: 'test', bind_interface: 'nocli0' };
	const outbounds = [
		test,
		{ type: 'direct' as const, tag: 'other', bind_interface: 'ppp0' },
		{ type: 'urltest' as const, tag: 'auto', outbounds: ['a', 'b'] }
	];
	const names = (l: { name: string }[]) => l.map((i) => i.name);

	it('edit keeps own bind, hides binds of other direct', () => {
		expect(names(directBindChoices(list, outbounds, test))).toEqual(['nocli0', 'ipsec0']);
	});
	it('add hides every taken bind', () => {
		expect(names(directBindChoices(list, outbounds))).toEqual(['ipsec0']);
	});
	it('own bind missing from the router list comes back as absent stub', () => {
		const got = directBindChoices(list.slice(1), outbounds, test);
		expect(names(got)).toEqual(['ipsec0', 'nocli0']);
		expect(got[1]).toMatchObject({ label: 'nocli0', up: false, absent: true });
	});
	it('own bind shared with another direct stays', () => {
		const twin = { type: 'direct' as const, tag: 'twin', bind_interface: 'nocli0' };
		expect(names(directBindChoices(list, [...outbounds, twin], test))).toContain('nocli0');
	});
});
