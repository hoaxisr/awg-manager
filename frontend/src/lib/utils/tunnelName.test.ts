import { describe, expect, it } from 'vitest';
import { tunnelNameBytes, tunnelNameError, TUNNEL_NAME_TOO_LONG } from './tunnelName';

describe('tunnelNameBytes', () => {
	it('считает байты UTF-8, а не символы', () => {
		expect(tunnelNameBytes('abc')).toBe(3);
		expect(tunnelNameBytes('ж')).toBe(2);
		expect(tunnelNameBytes('🇩🇪')).toBe(8);
	});
});

describe('tunnelNameError', () => {
	it('256 байт — предел включительно', () => {
		expect(tunnelNameError('ж'.repeat(128))).toBe('');
		expect(tunnelNameError('a'.repeat(256))).toBe('');
	});
	it('257 байт — текст сервера', () => {
		expect(tunnelNameError('ж'.repeat(128) + 'a')).toBe(TUNNEL_NAME_TOO_LONG);
		expect(TUNNEL_NAME_TOO_LONG).toBe('имя туннеля длиннее 256 байт (ограничение роутера)');
	});
	it('пустое имя не ошибка', () => {
		expect(tunnelNameError('')).toBe('');
	});
});
