import { describe, expect, it } from 'vitest';
import { restoresRoutingTab, routingStartTab } from './routingStartTab';

describe('стартовая вкладка «Маршрутизации»', () => {
	it.each([
		[null, null, false, 'firc'],
		[null, null, true, 'singbox'],
		['firc', null, true, 'firc'],
		[null, 'firc', true, 'firc'],
		['ip', 'dns', true, 'ip'],
		[null, 'dns', true, 'dns'],
	])('адрес %s, любимая %s, сторонний %s → %s', (url, favorite, foreign, want) => {
		expect(routingStartTab({ url, favorite }, foreign)).toBe(want);
	});

	it.each([
		[null, 'firc', true, false],
		[null, 'firc', false, true],
		[null, 'ip', true, true],
		['dns', 'ip', false, false],
	])('любимая %s, память %s, сторонний %s → восстанавливать: %s', (favorite, remembered, foreign, want) => {
		expect(restoresRoutingTab(favorite, remembered, foreign)).toBe(want);
	});
});
