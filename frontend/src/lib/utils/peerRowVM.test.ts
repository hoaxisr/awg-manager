import { describe, it, expect } from 'vitest';
import { stripHostMask, endpointHost, peerStatus, buildPeerRowVM, STATUS_LABEL } from './peerRowVM';
import type { ManagedPeer, ManagedPeerStats } from '$lib/types';

describe('stripHostMask', () => {
	it('срезает только /32', () => {
		expect(stripHostMask('10.20.0.2/32')).toBe('10.20.0.2');
	});
	it('не трогает прочие маски и голый ip', () => {
		expect(stripHostMask('10.20.0.0/24')).toBe('10.20.0.0/24');
		expect(stripHostMask('10.20.0.2')).toBe('10.20.0.2');
	});
});

describe('endpointHost', () => {
	it('убирает порт у ipv4', () => {
		expect(endpointHost('78.108.40.214:51820')).toBe('78.108.40.214');
	});
	it('убирает порт у ipv6 в скобках', () => {
		expect(endpointHost('[2001:db8::1]:51820')).toBe('[2001:db8::1]');
	});
	it('пустой/прочерк → тире', () => {
		expect(endpointHost('-')).toBe('—');
		expect(endpointHost('')).toBe('—');
		expect(endpointHost(undefined)).toBe('—');
	});
	it('голый ipv6 без порта оставляет как есть', () => {
		expect(endpointHost('2001:db8::1')).toBe('2001:db8::1');
	});
});

describe('peerStatus', () => {
	it('disabled важнее online', () => {
		expect(peerStatus(false, true)).toBe('disabled');
	});
	it('online/offline по флагу', () => {
		expect(peerStatus(true, true)).toBe('online');
		expect(peerStatus(true, false)).toBe('offline');
		expect(peerStatus(true, null)).toBe('offline');
	});
});

describe('buildPeerRowVM', () => {
	const peer: ManagedPeer = {
		publicKey: 'ABCDEFGH12345', privateKey: '', presharedKey: '',
		description: 'Mac', tunnelIP: '10.20.0.2/32', enabled: true,
	};
	const stats: ManagedPeerStats = {
		publicKey: 'ABCDEFGH12345', endpoint: '78.108.40.214:51820',
		rxBytes: 1024, txBytes: 2048, lastHandshake: '2026-06-13T12:00:00Z', online: true,
	};
	it('маппит поля, режет /32, убирает порт', () => {
		const vm = buildPeerRowVM(peer, stats);
		expect(vm.name).toBe('Mac');
		expect(vm.ip).toBe('10.20.0.2');
		expect(vm.endpointHost).toBe('78.108.40.214');
		expect(vm.status).toBe('online');
		expect(vm.enabled).toBe(true);
	});
	it('имя из ключа, если нет description', () => {
		const vm = buildPeerRowVM({ ...peer, description: '' }, undefined);
		expect(vm.name).toBe('ABCDEFGH...');
		expect(vm.endpointHost).toBe('—');
		expect(vm.status).toBe('offline');
	});
	it('STATUS_LABEL маппит в верхний регистр', () => {
		expect(STATUS_LABEL.online).toBe('ONLINE');
		expect(STATUS_LABEL.offline).toBe('OFFLINE');
		expect(STATUS_LABEL.disabled).toBe('OFF');
	});
});
