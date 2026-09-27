import { describe, expect, it } from 'vitest';
import { bindInterfaceLabel } from './bindInterface';

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
