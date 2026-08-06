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

/** Ядро в порядке: netfilter есть, xt_dscp пригоден. */
const KERNEL_OK = { xtDscpAvailable: true, netfilterAvailable: true };

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
		expect(
			netfilterExclusionsScope({ mode, qosClassCount: classes, ...KERNEL_OK }).applies,
		).toBe(applies);
	});

	it('в tproxy объяснять нечего — подписи нет', () => {
		expect(
			netfilterExclusionsScope({ mode: 'tproxy', qosClassCount: 0, ...KERNEL_OK }).notice,
		).toBeNull();
	});

	it.each(MODES.filter((m) => m !== 'tproxy'))(
		'в режиме %s подпись есть и называет режим',
		(mode) => {
			for (const classes of [0, 2]) {
				const notice = netfilterExclusionsScope({
					mode,
					qosClassCount: classes,
					...KERNEL_OK,
				}).notice;
				expect(notice).not.toBeNull();
				expect(notice).toContain(mode === 'fakeip-tun' ? 'FakeIP' : 'Политики + tun');
			}
		},
	);

	// Две подписи policy-tun различаются по смыслу, а не по вежливости: без
	// классов настройка не влияет ни на что, с классами — влияет, но только на
	// DSCP-меченый трафик. Одинаковый текст на обе ветки был бы полуправдой.
	it('в policy-tun подпись разная с классами QoS и без них', () => {
		const without = netfilterExclusionsScope({
			mode: 'policy-tun',
			qosClassCount: 0,
			...KERNEL_OK,
		}).notice;
		const with2 = netfilterExclusionsScope({
			mode: 'policy-tun',
			qosClassCount: 2,
			...KERNEL_OK,
		}).notice;
		expect(without).not.toBe(with2);
		expect(without).toMatch(/ни на что не влияют/);
		expect(with2).toMatch(/QoS/);
	});

	it('в FakeIP подпись говорит, что настройка не применяется', () => {
		expect(
			netfilterExclusionsScope({ mode: 'fakeip-tun', qosClassCount: 0, ...KERNEL_OK }).notice,
		).toMatch(/не применяются/);
	});

	// ── Состояние ядра (policytun_enable.go:337-350) ─────────────────────────
	//
	// Бэкенд обнуляет qosSpecs ДО проверки классов: сначала при недоступном
	// netfilter, затем при непригодном xt_dscp. Считать одни только классы —
	// та же ложь, что мы чинили режимным гейтом: «класс есть» при выключенном
	// модуле даёт спокойную подпись, хотя не поставлено ничего.
	it('в policy-tun с классом QoS, но без xt_dscp, исключения не применяются', () => {
		const scope = netfilterExclusionsScope({
			mode: 'policy-tun',
			qosClassCount: 1,
			xtDscpAvailable: false,
			netfilterAvailable: true,
		});
		expect(scope.applies).toBe(false);
		expect(scope.notice).toMatch(/xt_dscp/);
		expect(scope.notice).toMatch(/ни на что не влияют/);
	});

	it('в policy-tun с классом QoS, но без netfilter, исключения не применяются', () => {
		const scope = netfilterExclusionsScope({
			mode: 'policy-tun',
			qosClassCount: 1,
			xtDscpAvailable: true,
			netfilterAvailable: false,
		});
		expect(scope.applies).toBe(false);
		expect(scope.notice).toMatch(/netfilter на роутере недоступен/);
	});

	// Причина называется первая по порядку обнулений бэкенда — чинить надо её.
	it('нет ни netfilter, ни xt_dscp — названа причина «netfilter», а не «xt_dscp»', () => {
		const scope = netfilterExclusionsScope({
			mode: 'policy-tun',
			qosClassCount: 1,
			xtDscpAvailable: false,
			netfilterAvailable: false,
		});
		expect(scope.notice).toMatch(/netfilter на роутере недоступен/);
		expect(scope.notice).not.toMatch(/xt_dscp/);
	});

	// Тристейт: undefined — «бэкенд не сказал» (легаси-ответ), выдумывать
	// поломку нельзя. Тот же строгий `=== false`, что у QosSettingsCard.
	it('неизвестное состояние ядра не выдаётся за поломку', () => {
		const scope = netfilterExclusionsScope({ mode: 'policy-tun', qosClassCount: 1 });
		expect(scope.applies).toBe(true);
		expect(scope.notice).not.toMatch(/xt_dscp|netfilter на роутере/);
	});

	// В TPROXY bypass уходит в RestoreInputSpec независимо от классов и модуля
	// DSCP — приплетать сюда xt_dscp значило бы выдумать несуществующую связь.
	it('в tproxy состояние xt_dscp на исключения не влияет', () => {
		const scope = netfilterExclusionsScope({
			mode: 'tproxy',
			qosClassCount: 0,
			xtDscpAvailable: false,
			netfilterAvailable: true,
		});
		expect(scope.applies).toBe(true);
		expect(scope.notice).toBeNull();
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
