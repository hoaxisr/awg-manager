import { describe, it, expect } from 'vitest';
import type { FreeTurnServerConfig, WdttServerConfig } from '$lib/types';
import {
	freeTurnServerPorts,
	natModeLabel,
	normalizeWdttServerConfig,
	wdttServerPorts,
} from './shareConfig';

function wdtt(extra: Partial<WdttServerConfig> = {}): WdttServerConfig {
	return {
		listen: '0.0.0.0:56002',
		wgPort: 56001,
		password: 'secret',
		...extra,
	};
}

describe('normalizeWdttServerConfig: поля с omitempty', () => {
	it('заполняет строки, на которые смотрит bind:value', () => {
		const cfg = normalizeWdttServerConfig(wdtt());
		expect(cfg.configDir).toBe('');
		expect(cfg.adminId).toBe('');
		expect(cfg.botToken).toBe('');
	});

	it('не трогает union-поля: пустая строка сломала бы их семантику', () => {
		const cfg = normalizeWdttServerConfig(wdtt());
		expect(cfg.statsLog).toBeUndefined();
		expect(cfg.natMode).toBeUndefined();
	});
});

describe('wdttServerPorts', () => {
	it('Raw по умолчанию — следующий порт за DTLS', () => {
		const ports = wdttServerPorts(wdtt());
		expect(ports.map((p) => [p.label, p.port])).toEqual([
			['DTLS', 56002],
			['Raw', 56003],
		]);
	});

	it('заданный Raw-порт берётся как есть', () => {
		const ports = wdttServerPorts(wdtt({ rawListen: '0.0.0.0:56010' }));
		expect(ports[1].port).toBe(56010);
		expect(ports[1].listen).toBe('0.0.0.0:56010');
	});

	it('Direct не показывается, пока совпадает с DTLS', () => {
		const ports = wdttServerPorts(wdtt({ directListen: '0.0.0.0:56002' }));
		expect(ports.some((p) => p.label === 'Direct')).toBe(false);
	});

	it('отличный Direct-порт добавляет третью строку', () => {
		const ports = wdttServerPorts(wdtt({ directListen: '0.0.0.0:56005' }));
		expect(ports[2]).toMatchObject({ label: 'Direct', port: 56005 });
	});
});

describe('freeTurnServerPorts', () => {
	it('протокол порта — режим сервера', () => {
		const cfg = { listen: '0.0.0.0:56000', mode: 'tcp' } as FreeTurnServerConfig;
		expect(freeTurnServerPorts(cfg)[0]).toMatchObject({ port: 56000, proto: 'tcp' });
	});
});

describe('natModeLabel', () => {
	it('пустой режим — «Полный» (умолчание бэкенда)', () => {
		expect(natModeLabel(undefined)).toBe('Полный');
		expect(natModeLabel('internet-only')).toBe('Интернет');
		expect(natModeLabel('none')).toBe('Без NAT');
	});
});
