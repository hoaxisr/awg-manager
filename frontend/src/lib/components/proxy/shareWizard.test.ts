import { describe, it, expect } from 'vitest';
import type { FreeTurnServerConfig, WdttServerConfig } from '$lib/types';
import {
	nextSharePort,
	peerWithPort,
	rawPortHint,
	shareConfigSetupComplete,
	shareStep2Ready,
	wdttCardBlock,
} from './shareWizard';

describe('wdttCardBlock: доступность WDTT-карточки шага 1', () => {
	it('статус не загружен — карточка доступна (W-03)', () => {
		expect(wdttCardBlock({ serverExists: false })).toBeNull();
	});

	it('сервер уже есть — бейдж WS-11, подпись WS-12 и (i) WS-13', () => {
		const block = wdttCardBlock({ serverExists: true, serverSupported: true });
		expect(block?.badge).toBe('уже настроен');
		expect(block?.note).toBe('WDTT-сервер на роутере может быть только один');
		expect(block?.info).toContain('второй такой сервер не поднимется');
	});

	it('сервер не собран под процессор — WS-14 и WS-15, без подписи WS-12', () => {
		const block = wdttCardBlock({ serverExists: false, serverSupported: false });
		expect(block?.badge).toBe('недоступен на этом роутере');
		expect(block?.note).toBe('');
		expect(block?.info).toBe('Сервер WDTT не собран под процессор этого роутера.');
	});

	it('не собран важнее «уже есть»: инстанса без сборки быть не может', () => {
		const block = wdttCardBlock({ serverExists: true, serverSupported: false });
		expect(block?.badge).toBe('недоступен на этом роутере');
	});
});

describe('rawPortHint: WS-19 считается от введённого порта', () => {
	it('дефолтный порт даёт 56003', () => {
		expect(rawPortHint('56002')).toBe('Raw-половина займёт следующий порт — 56003');
	});

	it('порт изменён — подсказка пересчитана, а не зашита литералом', () => {
		expect(rawPortHint('40000')).toBe('Raw-половина займёт следующий порт — 40001');
	});

	it('недопечатанный порт — подсказка держится дефолта', () => {
		expect(rawPortHint('')).toBe('Raw-половина займёт следующий порт — 56003');
		expect(rawPortHint('65535')).toBe('Raw-половина займёт следующий порт — 56003');
	});
});

describe('shareStep2Ready: критерий старта бэкенда', () => {
	const base = { password: '', port: '56002', connect: '' };

	it('WDTT: без главного пароля дальше не пускает', () => {
		expect(shareStep2Ready({ ...base, protocol: 'wdtt' })).toBe(false);
		expect(shareStep2Ready({ ...base, protocol: 'wdtt', password: 'main' })).toBe(true);
	});

	it('FreeTurn: нужен backend-адрес, пароля у сервера нет', () => {
		expect(shareStep2Ready({ ...base, protocol: 'freeturn' })).toBe(false);
		expect(
			shareStep2Ready({ ...base, protocol: 'freeturn', connect: '127.0.0.1:51820' }),
		).toBe(true);
	});

	it('порт вне диапазона отвергается у обоих', () => {
		expect(shareStep2Ready({ protocol: 'wdtt', password: 'main', port: '0', connect: '' })).toBe(
			false,
		);
		expect(
			shareStep2Ready({
				protocol: 'freeturn',
				password: '',
				port: '70000',
				connect: '127.0.0.1:1',
			}),
		).toBe(false);
	});
});

describe('shareConfigSetupComplete: тот же критерий для сохранённого конфига', () => {
	it('WDTT-сервер настроен главным паролем', () => {
		const cfg = { listen: '0.0.0.0:56002', wgPort: 56001, password: '' } as WdttServerConfig;
		expect(shareConfigSetupComplete(cfg)).toBe(false);
		expect(shareConfigSetupComplete({ ...cfg, password: 'main' })).toBe(true);
	});

	it('FreeTurn-сервер настроен backend-адресом', () => {
		const cfg = {
			enabled: false,
			listen: '0.0.0.0:56000',
			connect: '',
			mode: 'udp',
			obfProfile: 'none',
			debug: false,
		} as FreeTurnServerConfig;
		expect(shareConfigSetupComplete(undefined, cfg)).toBe(false);
		expect(shareConfigSetupComplete(undefined, { ...cfg, connect: '127.0.0.1:51820' })).toBe(true);
	});
});

describe('nextSharePort: порт нового сервера', () => {
	it('FreeTurn берёт первый свободный из 56000..56099', () => {
		expect(nextSharePort('freeturn', [])).toBe(56000);
		expect(nextSharePort('freeturn', [56000, 56001])).toBe(56002);
	});

	it('WDTT — дефолт бинаря: сервер на роутере один', () => {
		expect(nextSharePort('wdtt', [56002])).toBe(56002);
	});
});

describe('peerWithPort: адрес ссылки', () => {
	it('голому адресу дописывается порт сервера', () => {
		expect(peerWithPort('203.0.113.10', 56002)).toBe('203.0.113.10:56002');
	});

	it('свой порт не перебивается, пустой адрес остаётся пустым', () => {
		expect(peerWithPort('example.org:1234', 56002)).toBe('example.org:1234');
		expect(peerWithPort('  ', 56002)).toBe('');
	});
});
