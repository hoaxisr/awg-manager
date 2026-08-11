import { describe, expect, it } from 'vitest';
import type { WdttClientConfig } from '$lib/types';
import { setPeer, setPeerRaw, setPeerWg, switchConnMode } from './wdttPeerMode';

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
		setPeer(c, '1.1.1.1:56003');

		switchConnMode(c, 'wg');
		expect(c.peer).toBe('1.1.1.1:56002');
		switchConnMode(c, 'raw');
		expect(c.peer).toBe('1.1.1.1:56003');
	});

	it('пустой слот даёт пустое поле, а не адрес соседнего режима', () => {
		const c = cfg({ peer: '1.1.1.1:56002', connMode: 'wg' });
		switchConnMode(c, 'raw');
		expect(c.peer).toBe('');
		expect(c.peerWg).toBe('1.1.1.1:56002');
	});

	it('правка активного слота уходит и в peer — иначе бэкенд её затрёт', () => {
		const c = cfg({ peer: '1.1.1.1:56002', connMode: 'wg' });
		setPeerWg(c, '2.2.2.2:56002');
		expect(c.peer).toBe('2.2.2.2:56002');
	});

	it('правка неактивного слота peer не трогает', () => {
		const c = cfg({ peer: '1.1.1.1:56002', connMode: 'wg' });
		setPeerRaw(c, '1.1.1.1:56003');
		expect(c.peer).toBe('1.1.1.1:56002');
		expect(c.peerRaw).toBe('1.1.1.1:56003');
	});
});
