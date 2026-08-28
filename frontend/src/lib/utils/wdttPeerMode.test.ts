import { describe, expect, it } from 'vitest';
import type { WdttClientConfig } from '$lib/types';
import { derivePeerForMode, setPeer, switchConnMode } from './wdttPeerMode';

function cfg(partial: Partial<WdttClientConfig> = {}): WdttClientConfig {
	return {
		listen: '127.0.0.1:9000',
		peer: '',
		password: '',
		vkHashes: '',
		workers: 24,
		obfs: 'audio',
		fingerprint: 'chrome',
		captchaMode: 'rjs',
		...partial
	};
}

describe('wdttPeerMode', () => {
	it('переключение режима возвращает адрес, введённый для этого режима', () => {
		const c = cfg({ peer: '1.1.1.1:56002', connMode: 'wg' });
		switchConnMode(c, 'raw');
		setPeer(c, '1.1.1.1:60003');

		switchConnMode(c, 'wg');
		expect(c.peer).toBe('1.1.1.1:56002');
		switchConnMode(c, 'raw');
		expect(c.peer).toBe('1.1.1.1:60003');
	});

	// В ссылке wdtt:// лежит только DTLS-порт, поэтому у всех, кто импортировал
	// профиль, raw-слот пуст. Пустое поле после переключения давало мёртвый
	// инстанс: клиент падал на «не задан адрес сервера» (стенд 2026-08-28).
	it('пустой слот заполняется по конвенции портов: raw = DTLS+1', () => {
		const c = cfg({ peer: '1.1.1.1:56002', connMode: 'wg' });
		switchConnMode(c, 'raw');
		expect(c.peer).toBe('1.1.1.1:56003');
		expect(c.peerWg).toBe('1.1.1.1:56002');
	});

	it('обратно из raw в wg порт уменьшается на единицу', () => {
		const c = cfg({ peer: '2.2.2.2:56003', connMode: 'raw' });
		switchConnMode(c, 'wg');
		expect(c.peer).toBe('2.2.2.2:56002');
	});

	it('введённый руками адрес режима не перетирается вычисленным', () => {
		const c = cfg({ peer: '1.1.1.1:56002', connMode: 'wg', peerRaw: '9.9.9.9:7777' });
		switchConnMode(c, 'raw');
		expect(c.peer).toBe('9.9.9.9:7777');
	});

	it('вычислять не из чего — поле остаётся пустым', () => {
		expect(derivePeerForMode('', 'raw')).toBe('');
		expect(derivePeerForMode('example.com', 'raw')).toBe('');
		expect(derivePeerForMode('1.1.1.1:65535', 'raw')).toBe('');
	});

	it('IPv6 в скобках сохраняет форму', () => {
		expect(derivePeerForMode('[2001:db8::1]:56002', 'raw')).toBe('[2001:db8::1]:56003');
	});
});
