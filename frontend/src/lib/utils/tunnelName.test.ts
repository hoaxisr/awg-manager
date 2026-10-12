import { describe, expect, it } from 'vitest';
import { tunnelNameBytes, tunnelNameError, TUNNEL_NAME_MAX_BYTES } from './tunnelName';
import { m } from '$lib/i18n';

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
	it('257 байт — ошибка с пределом роутера', () => {
		expect(tunnelNameError('ж'.repeat(128) + 'a')).toBe(
			m.validation_tunnel_name_too_long({ max: TUNNEL_NAME_MAX_BYTES }),
		);
	});
	it('пустое имя не ошибка', () => {
		expect(tunnelNameError('')).toBe('');
	});
});
