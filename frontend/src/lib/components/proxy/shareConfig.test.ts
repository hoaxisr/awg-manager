import { describe, it, expect } from 'vitest';
import type { FreeTurnServerConfig, WdttServerConfig } from '$lib/types';
import {
	directListenValue,
	freeTurnServerPorts,
	natModeLabel,
	serverPortConflict,
	normalizeWdttServerConfig,
	wdttServerKillPorts,
	wdttServerPorts,
} from './shareConfig';

function wdtt(extra: Partial<WdttServerConfig> = {}): WdttServerConfig {
	return {
		listen: '0.0.0.0:56002',
		wgPort: 56001,
		...extra,
	};
}

describe('normalizeWdttServerConfig: поля с omitempty', () => {
	it('заполняет строки, на которые смотрит bind:value', () => {
		const cfg = normalizeWdttServerConfig(wdtt());
		expect(cfg.configDir).toBe('');
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

	it('строк ровно две, пока Direct выключен', () => {
		expect(wdttServerPorts(wdtt()).map((p) => p.label)).toEqual(['DTLS', 'Raw']);
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

describe('wdttServerKillPorts', () => {
	it('WG-порт добавляется отдельной строкой', () => {
		const ports = wdttServerKillPorts(wdtt());
		expect(ports.map((p) => [p.label, p.port])).toEqual([
			['DTLS', 56002],
			['Raw', 56003],
			['WG', 56001],
		]);
	});

	it('совпавший с Raw WG-порт — одна строка (ключ each не дублируется)', () => {
		// DTLS :56000 → Raw по умолчанию :56001 == дефолтный WG-порт.
		const ports = wdttServerKillPorts(wdtt({ listen: '0.0.0.0:56000', wgPort: 56001 }));
		expect(ports.map((p) => p.listen)).toEqual(['0.0.0.0:56000', '0.0.0.0:56001']);
		expect(new Set(ports.map((p) => p.listen)).size).toBe(ports.length);
	});

	it('совпавшие DTLS и Raw тоже схлопываются в одну строку', () => {
		const ports = wdttServerKillPorts(wdtt({ rawListen: '0.0.0.0:56002' }));
		expect(new Set(ports.map((p) => p.listen)).size).toBe(ports.length);
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

// Та же проверка, что у бэкенда (`WdttServerConfig.validatePorts`): создать
// столкновение из UI нельзя, а не «бэкенд потом откажет».
describe('serverPortConflict', () => {
	it('дефолтная раскладка конфликта не даёт', () => {
		expect(serverPortConflict(wdtt({ wgPort: 56001 }))).toBe('');
	});

	it('ловит raw по умолчанию, налетевший на дефолтный WG', () => {
		// Порт раздачи 56000 → raw получает 56001, там уже стоит WG.
		const msg = serverPortConflict(wdtt({ listen: '0.0.0.0:56000', wgPort: 56001 }));
		expect(msg).toContain('56001');
	});

	it('ловит явный raw, равный порту раздачи', () => {
		const msg = serverPortConflict(
			wdtt({ listen: '0.0.0.0:56002', rawListen: '0.0.0.0:56002', wgPort: 56001 }),
		);
		expect(msg).toContain('56002');
	});

	it('direct, равный порту раздачи, — это «выключено»', () => {
		expect(
			serverPortConflict(
				wdtt({ listen: '0.0.0.0:56002', directListen: '0.0.0.0:56002', wgPort: 56001 }),
			),
		).toBe('');
	});
});

// Поле «Порт Direct» принимает НОМЕР порта; адрес собирается здесь. Пока поле
// было свободным текстом, из него уходили значения, которые бэкенд читал иначе
// (`validatePorts`, internal/proxyrt/roles/config.go).
describe('directListenValue', () => {
	it('пусто — «выключено»', () => {
		expect(directListenValue('0.0.0.0:56002', '')).toBe('');
		expect(directListenValue('0.0.0.0:56002', '   ')).toBe('');
	});

	it('порт, равный порту раздачи, даёт строку, побайтово равную listen', () => {
		// Именно это равенство читает бэкенд как «выключено» (`d != c.Listen`).
		expect(directListenValue('0.0.0.0:56002', '56002')).toBe('0.0.0.0:56002');
		expect(directListenValue('127.0.0.1:56002', '56002')).toBe('127.0.0.1:56002');
	});

	it('хост наследуется от порта раздачи — адрес без хоста невыразим', () => {
		expect(directListenValue('10.1.2.3:56002', '56005')).toBe('10.1.2.3:56005');
		expect(directListenValue('', '56005')).toBe('0.0.0.0:56005');
		expect(directListenValue(undefined, '56005')).toBe('0.0.0.0:56005');
	});

	it('нечисло и неположительный порт ввод не принимают', () => {
		expect(directListenValue('0.0.0.0:56002', 'abc')).toBeNull();
		expect(directListenValue('0.0.0.0:56002', '0')).toBeNull();
		expect(directListenValue('0.0.0.0:56002', '-1')).toBeNull();
	});

	it('порт зажат сверху потолком 65535', () => {
		expect(directListenValue('0.0.0.0:56002', '99999')).toBe('0.0.0.0:65535');
	});

	it('нормализованный direct согласован с детектором конфликта', () => {
		const listen = '0.0.0.0:56002';
		// Свободный порт: конфликта нет ни у фронта, ни у validatePorts.
		const free = directListenValue(listen, '56005');
		expect(serverPortConflict(wdtt({ listen, directListen: free!, wgPort: 56001 }))).toBe('');
		// Порт raw-половины (DTLS+1) занят — оба гарда видят столкновение.
		const busy = directListenValue(listen, '56003');
		const msg = serverPortConflict(wdtt({ listen, directListen: busy!, wgPort: 56001 }));
		expect(msg).toContain('56003');
		// Равный порту раздачи — «выключено», третьей строки портов нет.
		const off = directListenValue(listen, '56002');
		expect(off).toBe(listen);
		expect(wdttServerPorts(wdtt({ listen, directListen: off! })).map((p) => p.label)).toEqual([
			'DTLS',
			'Raw',
		]);
	});
});

describe('directListen, записанный до нормализации поля', () => {
	it('голый порт превращается в адрес — иначе он уедет в освобождение портов как есть', () => {
		const ports = wdttServerPorts(wdtt({ listen: '0.0.0.0:56002', directListen: '56005' }));
		expect(ports.find((p) => p.label === 'Direct')?.listen).toBe('0.0.0.0:56005');
	});

	it('хост берётся от порта раздачи, как у строк DTLS и Raw', () => {
		const ports = wdttServerPorts(wdtt({ listen: '127.0.0.1:56002', directListen: '56005' }));
		expect(ports.find((p) => p.label === 'Direct')?.listen).toBe('127.0.0.1:56005');
	});
});

describe('serverPortConflict: пустой порт раздачи', () => {
	it('отвергается здесь, а не отказом бэкенда', () => {
		expect(serverPortConflict(wdtt({ listen: '', wgPort: 56001 }))).toContain('порт раздачи');
		expect(serverPortConflict(wdtt({ listen: '   ', wgPort: 56001 }))).toContain('порт раздачи');
	});
});
