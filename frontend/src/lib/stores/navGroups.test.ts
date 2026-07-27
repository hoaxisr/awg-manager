import { describe, it, expect } from 'vitest';
import { get } from 'svelte/store';
import { navGroups } from './navGroups';

describe('navGroups', () => {
	it('дефолт: все группы развёрнуты', () => {
		const v = get(navGroups);
		expect(v).toEqual({ awg: true, sb: true, router: true, services: true });
	});
	it('toggle сворачивает и разворачивает', () => {
		navGroups.toggle('router');
		expect(get(navGroups).router).toBe(false);
		expect(get(navGroups).awg).toBe(true);
		navGroups.toggle('router');
		expect(get(navGroups).router).toBe(true);
	});
});
