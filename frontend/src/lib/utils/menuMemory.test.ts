import { describe, it, expect, beforeEach } from 'vitest';
import { menuMemoryKey, readMenuChild, rememberMenuChild } from './menuMemory';

describe('menuMemory', () => {
	beforeEach(() => {
		localStorage.clear();
	});

	it('ключ не зависит от порядка детей', () => {
		expect(menuMemoryKey('Sing-box', ['b', 'a'])).toBe(menuMemoryKey('Sing-box', ['a', 'b']));
	});

	it('одинаковые label с разным составом детей не сталкиваются', () => {
		expect(menuMemoryKey('Sing-box', ['a'])).not.toBe(menuMemoryKey('Sing-box', ['a', 'b']));
	});

	it('пишет и читает выбранного ребёнка', () => {
		rememberMenuChild('Sing-box', ['a', 'b'], 'b');
		expect(readMenuChild('Sing-box', ['a', 'b'])).toBe('b');
	});

	it('пишет в пространство ui.tabs.menuLast: — общее с Tabs', () => {
		rememberMenuChild('Sing-box', ['a'], 'a');
		const keys = Object.keys(localStorage);
		expect(keys.some((k) => k.startsWith('ui.tabs.menuLast:'))).toBe(true);
	});

	it('чужой id игнорируется при записи и при чтении', () => {
		rememberMenuChild('Sing-box', ['a', 'b'], 'zzz');
		expect(readMenuChild('Sing-box', ['a', 'b'])).toBeNull();

		localStorage.setItem(menuMemoryKey('Sing-box', ['a', 'b']), 'zzz');
		expect(readMenuChild('Sing-box', ['a', 'b'])).toBeNull();
	});

	it('без совпадения возвращает null', () => {
		expect(readMenuChild('Роутер', ['x'])).toBeNull();
	});
});
