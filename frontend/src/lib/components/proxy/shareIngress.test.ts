import { describe, it, expect } from 'vitest';
import type { WdttProcessStatus, WdttServerConfig } from '$lib/types';
import { ingressOn, nextIngressInterfaces, wdttIngressRefs } from './shareIngress';

const cfg = {
	listen: '0.0.0.0:56002',
	wgPort: 56001,
	password: 'secret',
	wgIface: 'opkgtun17',
} as WdttServerConfig;

const status = { rawIface: 'opkgtun18' } as WdttProcessStatus;

describe('wdttIngressRefs', () => {
	it('обе половины сервера по kernel-именам', () => {
		expect(wdttIngressRefs(cfg, status)).toEqual(['iface:opkgtun17', 'iface:opkgtun18']);
	});

	it('без имён — легаси-умолчания бэкенда', () => {
		expect(wdttIngressRefs({} as WdttServerConfig)).toEqual(['iface:wdtt0', 'iface:wdttraw0']);
	});
});

describe('ingressOn', () => {
	it('включён, если заведена хотя бы одна половина', () => {
		const refs = wdttIngressRefs(cfg, status);
		expect(ingressOn(['iface:opkgtun17'], refs)).toBe(true);
		expect(ingressOn(['managed:Wireguard3'], refs)).toBe(false);
		expect(ingressOn(undefined, refs)).toBe(false);
	});
});

describe('nextIngressInterfaces', () => {
	it('включение дописывает обе половины, чужие записи остаются', () => {
		const refs = wdttIngressRefs(cfg, status);
		expect(nextIngressInterfaces(['managed:Wireguard3'], refs, true)).toEqual([
			'managed:Wireguard3',
			'iface:opkgtun17',
			'iface:opkgtun18',
		]);
	});

	it('повторное включение не плодит дублей', () => {
		const refs = wdttIngressRefs(cfg, status);
		expect(nextIngressInterfaces(['iface:opkgtun17'], refs, true)).toEqual([
			'iface:opkgtun17',
			'iface:opkgtun18',
		]);
	});

	it('выключение убирает обе половины и не трогает остальные', () => {
		const refs = wdttIngressRefs(cfg, status);
		expect(
			nextIngressInterfaces(['iface:opkgtun17', 'iface:opkgtun18', 'iface:nwg3'], refs, false),
		).toEqual(['iface:nwg3']);
	});
});
