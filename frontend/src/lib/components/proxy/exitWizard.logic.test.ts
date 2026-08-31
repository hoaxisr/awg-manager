import { describe, it, expect } from 'vitest';
import {
	DEFAULT_FT_STREAMS,
	DEFAULT_WORKERS,
	exitConfigSetupComplete,
	emptyFields,
	exitStep1Ready,
	exitStep2Ready,
	fieldsFromFtPayload,
	fieldsFromWdttPayload,
	policyPermitOrder,
	proxyTunnelName,
} from './exitWizard';
import type { AccessPolicy } from '$lib/types';

describe('exitStep1Ready', () => {
	it('WDTT требует и адрес, и пароль', () => {
		const base = { manual: false, protocol: 'wdtt' as const };
		expect(exitStep1Ready({ ...base, peer: 'vps:56000', password: 'x' })).toBe(true);
		expect(exitStep1Ready({ ...base, peer: 'vps:56000', password: '' })).toBe(false);
		expect(exitStep1Ready({ ...base, peer: '', password: 'x' })).toBe(false);
	});

	it('FreeTurn обходится адресом: пароля у клиента нет', () => {
		const base = { manual: false, protocol: 'freeturn' as const };
		expect(exitStep1Ready({ ...base, peer: 'vps:56000', password: '' })).toBe(true);
		expect(exitStep1Ready({ ...base, peer: '   ', password: '' })).toBe(false);
	});

	it('ручное создание готово выбором протокола: поля спрашивают на шаге 2', () => {
		expect(exitStep1Ready({ manual: true, protocol: 'wdtt', peer: '', password: '' })).toBe(true);
		expect(exitStep1Ready({ manual: true, protocol: 'freeturn', peer: '', password: '' })).toBe(
			true,
		);
	});
});

describe('exitStep2Ready', () => {
	const wdtt = {
		protocol: 'wdtt' as const,
		peer: 'vps:56000',
		password: 'pass',
		vkHashes: 'aa,bb',
		workers: '27',
	};

	it('WDTT: адрес, пароль, VK-хеши и положительные потоки', () => {
		expect(exitStep2Ready(wdtt)).toBe(true);
		expect(exitStep2Ready({ ...wdtt, password: '' })).toBe(false);
		expect(exitStep2Ready({ ...wdtt, vkHashes: '' })).toBe(false);
		expect(exitStep2Ready({ ...wdtt, workers: '0' })).toBe(false);
		expect(exitStep2Ready({ ...wdtt, workers: '' })).toBe(false);
		expect(exitStep2Ready({ ...wdtt, peer: ' ' })).toBe(false);
	});

	it('FreeTurn: пароля нет, но ссылки VK Calls обязательны', () => {
		const ft = { ...wdtt, protocol: 'freeturn' as const, password: '' };
		expect(exitStep2Ready(ft)).toBe(true);
		expect(exitStep2Ready({ ...ft, vkHashes: '' })).toBe(false);
	});
});

describe('exitConfigSetupComplete', () => {
	it('судит сохранённый конфиг тем же критерием, что шаг 2', () => {
		const wdtt = {
			peer: 'vps:56000',
			password: 'p',
			vkHashes: 'aa',
			workers: 27,
		} as unknown as Parameters<typeof exitConfigSetupComplete>[0];
		expect(exitConfigSetupComplete(wdtt)).toBe(true);
		expect(exitConfigSetupComplete({ ...wdtt!, vkHashes: '' })).toBe(false);
		expect(exitConfigSetupComplete(undefined, undefined)).toBe(false);
	});

	it('FreeTurn: ссылки VK Calls вместо пароля', () => {
		const ft = { peer: 'vps:56000', links: 'https://vk', streams: 10 } as unknown as Parameters<
			typeof exitConfigSetupComplete
		>[1];
		expect(exitConfigSetupComplete(undefined, ft)).toBe(true);
		expect(exitConfigSetupComplete(undefined, { ...ft!, links: '' })).toBe(false);
	});
});

describe('поля из ссылки', () => {
	it('WDTT-профиль: хеши через запятую, потоки из ссылки', () => {
		const f = fieldsFromWdttPayload(
			{
				name: 'Амстердам',
				peer: 'nl:56000',
				password: 'p',
				vkHashes: ['aa', 'bb'],
				workers: 18,
				listen: '127.0.0.1:9005',
			},
		);
		// Порт из ссылки в поля не попадает вовсе: он адресован приложению
		// qWDTT, а инстансу порт выдаёт бэкенд.
		expect(f).toEqual({
			name: 'Амстердам',
			peer: 'nl:56000',
			password: 'p',
			vkHashes: 'aa,bb',
			workers: '18',
		});
	});

	it('FreeTurn: пароля нет, ссылки VK Calls заполняются руками', () => {
		const f = fieldsFromFtPayload({ v: 1, name: 'FT', peer: 'vps:56000', n: 9 });
		expect(f.password).toBe('');
		expect(f.vkHashes).toBe('');
		expect(f.workers).toBe('9');
	});

	it('ручное создание — пустые поля и дефолт потоков', () => {
		expect(emptyFields()).toEqual({
			name: '',
			peer: '',
			password: '',
			vkHashes: '',
			workers: DEFAULT_WORKERS,
		});
	});

	it('дефолт потоков FreeTurn — дефолт бинаря, не wdtt-округление', () => {
		expect(emptyFields('freeturn').workers).toBe(DEFAULT_FT_STREAMS);
		expect(DEFAULT_FT_STREAMS).toBe('10');
		const f = fieldsFromFtPayload({ v: 1, peer: 'vps:56000' });
		expect(f.workers).toBe(DEFAULT_FT_STREAMS);
	});
});

describe('proxyTunnelName', () => {
	it('суффикс протокола не дублируется', () => {
		expect(proxyTunnelName('wdtt', 'Амстердам')).toBe('Амстердам wdtt');
		expect(proxyTunnelName('wdtt', 'Амстердам wdtt')).toBe('Амстердам wdtt');
		expect(proxyTunnelName('freeturn', 'Прага')).toBe('Прага FT');
		expect(proxyTunnelName('freeturn', 'Прага ft')).toBe('Прага ft');
	});

	it('пустое имя и обрезка до 60 символов', () => {
		expect(proxyTunnelName('wdtt', '  ')).toBe('WDTT wdtt');
		expect(proxyTunnelName('wdtt', 'я'.repeat(80))).toHaveLength(60);
	});
});

describe('policyPermitOrder', () => {
	const policies = [
		{ name: 'Policy0', interfaces: [{ name: 'a', order: 0 }, { name: 'b', order: 1 }] },
		{ name: 'Policy1', interfaces: [] },
	] as unknown as AccessPolicy[];

	it('интерфейс дописывается в конец порядка политики', () => {
		expect(policyPermitOrder(policies, 'Policy0')).toBe(2);
		expect(policyPermitOrder(policies, 'Policy1')).toBe(0);
		expect(policyPermitOrder(policies, 'нет такой')).toBe(0);
	});
});
