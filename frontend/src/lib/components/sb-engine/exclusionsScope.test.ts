import { describe, it, expect } from 'vitest';
import {
	DNS_BYPASS_PRESET_IDS,
	isDnsBypassPreset,
	netfilterExclusionsScope,
	partitionBypassPresets,
} from './exclusionsScope';
import { BYPASS_PRESETS } from '$lib/components/sb-router/settingsActions';
import type { EngineRoutingMode } from './engineRunState';

const MODES: EngineRoutingMode[] = ['tproxy', 'policy-tun', 'fakeip-tun'];

describe('netfilterExclusionsScope', () => {
	// Ядро задачи: карточка «Исключения» врала в двух режимах из трёх. Ассерт
	// именно на все три сразу — гейт, написанный как `!policyTunMode` (условие
	// шторки), прошёл бы проверку только по tproxy/policy-tun и оставил бы
	// FakeIP враньём.
	it.each([
		['tproxy', 0, true],
		['tproxy', 3, true],
		['policy-tun', 0, false],
		['policy-tun', 1, true],
		['fakeip-tun', 0, false],
		// В FakeIP netfilter-исключений нет НЕЗАВИСИМО от классов QoS: QoS там и
		// сам недоступен, а вызовов resolveBypassPorts на fakeip-пути нет вовсе.
		['fakeip-tun', 5, false],
	] as const)('%s с %i классами QoS: applies=%s', (mode, classes, applies) => {
		expect(netfilterExclusionsScope(mode, classes).applies).toBe(applies);
	});

	it('в tproxy объяснять нечего — подписи нет', () => {
		expect(netfilterExclusionsScope('tproxy', 0).notice).toBeNull();
	});

	it.each(MODES.filter((m) => m !== 'tproxy'))(
		'в режиме %s подпись есть и называет режим',
		(mode) => {
			for (const classes of [0, 2]) {
				const notice = netfilterExclusionsScope(mode, classes).notice;
				expect(notice).not.toBeNull();
				expect(notice).toContain(mode === 'fakeip-tun' ? 'FakeIP' : 'Политики + tun');
			}
		},
	);

	// Две подписи policy-tun различаются по смыслу, а не по вежливости: без
	// классов настройка не влияет ни на что, с классами — влияет, но только на
	// DSCP-меченый трафик. Одинаковый текст на обе ветки был бы полуправдой.
	it('в policy-tun подпись разная с классами QoS и без них', () => {
		const without = netfilterExclusionsScope('policy-tun', 0).notice;
		const with2 = netfilterExclusionsScope('policy-tun', 2).notice;
		expect(without).not.toBe(with2);
		expect(without).toMatch(/ни на что не влияют/);
		expect(with2).toMatch(/QoS/);
	});

	it('в FakeIP подпись говорит, что настройка не применяется', () => {
		expect(netfilterExclusionsScope('fakeip-tun', 0).notice).toMatch(/не применяются/);
	});
});

describe('partitionBypassPresets', () => {
	it('keendns — DNS-перезапись, остальные пресеты netfilter', () => {
		const { dns, netfilter } = partitionBypassPresets(BYPASS_PRESETS);
		expect(dns.map((p) => p.id)).toEqual(['keendns']);
		expect(netfilter.map((p) => p.id)).toEqual(['l2tp', 'ntp', 'netbios-smb']);
	});

	it('ни один пресет не теряется при разводке', () => {
		const { dns, netfilter } = partitionBypassPresets(BYPASS_PRESETS);
		expect([...dns, ...netfilter].map((p) => p.id).sort()).toEqual(
			BYPASS_PRESETS.map((p) => p.id).sort(),
		);
	});

	// Пресет мог быть переименован в settingsActions, а список DNS-пресетов —
	// нет: тогда keendns молча уехал бы в netfilter-блок и получил бы чужую
	// подпись «в FakeIP не применяется», хотя работает.
	it('каждый DNS-пресет существует в общем списке', () => {
		for (const id of DNS_BYPASS_PRESET_IDS) {
			expect(BYPASS_PRESETS.some((p) => p.id === id)).toBe(true);
			expect(isDnsBypassPreset(id)).toBe(true);
		}
	});
});
