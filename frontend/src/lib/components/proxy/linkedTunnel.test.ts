import { describe, it, expect } from 'vitest';
import type { TunnelListItem } from '$lib/types';
import { findLinkedTunnel, listenPort } from './linkedTunnel';

function tunnel(id: string, endpoint: string): TunnelListItem {
	return {
		id,
		name: id,
		type: 'amneziawg',
		status: 'running',
		enabled: true,
		endpoint,
		address: '10.0.0.2/32',
		pingCheck: { status: 'disabled', restartCount: 0, failCount: 0, failThreshold: 0 },
	};
}

describe('listenPort', () => {
	it('берёт порт из host:port', () => {
		expect(listenPort('127.0.0.1:9000')).toBe('9000');
	});

	it('пустое и бессмысленное значение — null', () => {
		expect(listenPort('')).toBeNull();
		expect(listenPort(undefined)).toBeNull();
		expect(listenPort('127.0.0.1')).toBeNull();
		expect(listenPort('127.0.0.1:порт')).toBeNull();
	});
});

describe('findLinkedTunnel', () => {
	const list = [
		tunnel('remote', 'de-fra.example:51820'),
		tunnel('linked', '127.0.0.1:9000'),
		tunnel('other-local', '127.0.0.1:9001'),
	];

	it('находит туннель по локальному порту клиента', () => {
		expect(findLinkedTunnel(list, '127.0.0.1:9000')?.id).toBe('linked');
	});

	it('различает соседние инстансы по порту', () => {
		expect(findLinkedTunnel(list, '127.0.0.1:9001')?.id).toBe('other-local');
	});

	it('чужой Endpoint с тем же портом не считается связанным', () => {
		expect(findLinkedTunnel([tunnel('remote', 'vps.example:9000')], '127.0.0.1:9000')).toBeNull();
	});

	it('без listen и без совпадения — null', () => {
		expect(findLinkedTunnel(list, '')).toBeNull();
		expect(findLinkedTunnel(list, '127.0.0.1:9999')).toBeNull();
	});
});
