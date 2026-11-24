import { describe, expect, it } from 'vitest';
import type { FircHost, FircPolicy } from '$lib/types/firc';
import { hasPolicyEntry, hostDeviceState, hostsByIp, policyLabel, policyOfEntry, withPolicyEntry } from './fircPolicyDevices';

const policies: FircPolicy[] = [
	{ name: 'Policy1', description: 'Дом', devices: 1 },
	{ name: 'Policy2', description: 'Дом', devices: 1 },
	{ name: 'Policy3', devices: 0 },
];

function host(over: Partial<FircHost>): FircHost {
	return { name: 'PC', mac: 'aa:bb:cc:dd:ee:01', ip: '192.168.1.50', ip6: [], ...over };
}

describe('policyOfEntry', () => {
	it('понимает имя и описание, в том числе с пробелами по краям', () => {
		expect(policyOfEntry('policy:Policy2', policies)).toBe('Policy2');
		expect(policyOfEntry('policy:Policy3', policies)).toBe('Policy3');
		expect(policyOfEntry('policy:Дом', policies)).toBe('Policy1');
		expect(policyOfEntry('policy:VPN', [{ name: 'Policy5', description: ' VPN ', devices: 0 }])).toBe('Policy5');
		expect(policyOfEntry('policy:Unknown', policies)).toBe('Unknown');
		expect(policyOfEntry('mac:aa:bb:cc:dd:ee:01', policies)).toBeNull();
	});

	it('общее описание принадлежит только первой политике', () => {
		expect(hasPolicyEntry(['policy:Дом'], 'Policy1', policies)).toBe(true);
		expect(hasPolicyEntry(['policy:Дом'], 'Policy2', policies)).toBe(false);
	});
});

describe('withPolicyEntry', () => {
	it('снимает все записи своей политики и не трогает чужие', () => {
		const entries = new Set(['policy:Дом', 'policy:Policy1', 'policy:Policy2', '10.0.0.1']);
		expect([...withPolicyEntry(entries, 'Policy1', policies, false, new Set())]).toEqual(['policy:Policy2', '10.0.0.1']);
		expect([...withPolicyEntry(new Set(['policy:Дом']), 'Policy2', policies, false, new Set())]).toEqual(['policy:Дом']);
	});

	it('новую запись пишет именем, снятую и возвращённую — как была', () => {
		expect([...withPolicyEntry(new Set<string>(), 'Policy1', policies, true, new Set())]).toEqual(['policy:Policy1']);
		expect([...withPolicyEntry(new Set<string>(), 'Policy1', policies, true, new Set(['policy:Дом']))]).toEqual(['policy:Дом']);
		expect([...withPolicyEntry(new Set<string>(), 'Policy2', policies, true, new Set(['policy:Дом']))]).toEqual(['policy:Policy2']);
	});
});

describe('hostDeviceState', () => {
	const key = 'mac:aa:bb:cc:dd:ee:01';

	it('политика решает за устройство в обоих режимах', () => {
		const h = host({ policy: ' Policy1 ' });
		expect(hostDeviceState(h, key, 'all', new Set(['policy:Policy1']), policies)).toEqual({ enabled: false, locked: true });
		expect(hostDeviceState(h, key, 'allow', new Set(['policy:Дом']), policies)).toEqual({ enabled: true, locked: true });
		expect(hostDeviceState(host({ policy: 'Policy2' }), key, 'all', new Set(['policy:Дом']), policies)).toEqual({ enabled: true, locked: false });
	});

	it('без политики решает собственная запись', () => {
		expect(hostDeviceState(host({}), key, 'all', new Set([key]), policies)).toEqual({ enabled: false, locked: false });
		expect(hostDeviceState(host({}), key, 'allow', new Set([key]), policies)).toEqual({ enabled: true, locked: false });
		expect(hostDeviceState(host({}), key, 'allow', new Set(), policies)).toEqual({ enabled: false, locked: false });
	});

	it('политика не из загруженного списка не блокирует', () => {
		const h = host({ policy: 'Policy1' });
		expect(hostDeviceState(h, key, 'all', new Set(['policy:Policy1']), [])).toEqual({ enabled: true, locked: false });
	});
});

describe('policyLabel', () => {
	it('показывает описание, а без него — имя', () => {
		expect(policyLabel('Policy1', policies)).toBe('Дом');
		expect(policyLabel('Policy3', policies)).toBe('Policy3');
		expect(policyLabel('Policy9', policies)).toBe('Policy9');
	});
});

describe('hostsByIp', () => {
	it('соединяет по IPv4 и IPv6, активное устройство важнее', () => {
		const stale = host({ name: 'Stale', ip6: ['2001:db8::50'], active: false });
		const live = host({ name: 'Live', ip6: ['2001:db8::50'], active: true });
		for (const order of [[stale, live], [live, stale]]) {
			const map = hostsByIp(order);
			expect(map.get('192.168.1.50')?.name).toBe('Live');
			expect(map.get('2001:db8::50')?.name).toBe('Live');
		}
	});

	it('пустой и нулевой адреса не ключи', () => {
		expect(hostsByIp([host({ ip: '0.0.0.0' }), host({ ip: '' })]).size).toBe(0);
	});
});
