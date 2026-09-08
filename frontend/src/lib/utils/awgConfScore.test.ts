import { describe, it, expect } from 'vitest';
import { scoreConfig, buildFixes } from './awgConfScore';
import type { AwgAnalyzeData } from '$lib/types';

const HP_KEY_OK = true;

function base(over: Partial<AwgAnalyzeData['interface']> = {}, peer: Partial<AwgAnalyzeData['peer']> = {}, top: Partial<AwgAnalyzeData> = {}): AwgAnalyzeData {
	return {
		version: 'awg1.0',
		interface: {
			jc: 4, jmin: 50, jmax: 1000, s1: 15, s2: 30, s3: 0, s4: 0,
			h1: '10', h2: '20', h3: '30', h4: '40',
			i1: '', i2: '', i3: '', i4: '', i5: '',
			headerProtection: false, contentPaddingAddition: '',
			rekeyAfterTime: '', rekeyTimeout: '', rejectAfterTime: '', keepaliveTimeout: '', maxHandshakeAttempts: '',
			randomTrailers: false, disableCookies: false,
			mtu: 1420, mtuSet: true, address: '10.8.0.2/32', dns: '',
			...over,
		},
		peer: { endpoint: 'vpn.example.com:4443', allowedIPs: ['0.0.0.0/0'], allowedIPsSet: true, persistentKeepalive: '25', keepaliveSet: true, hasPresharedKey: false, presharedKeyFromStore: false, ...peer },
		errors: [],
		warnings: [],
		...top,
	};
}

const find = (r: ReturnType<typeof scoreConfig>, title: string) => r.checks.find((c) => c.title === title)!;

describe('scoreConfig — база профиля', () => {
	it('wg = 0, awg1.0 = 40, awg1.5 = 55, awg2.0 = 65', () => {
		expect(scoreConfig(base({}, {}, { version: 'wg' })).base).toBe(0);
		expect(scoreConfig(base()).base).toBe(40);
		expect(scoreConfig(base({ i1: '<b 0xc0000001>' }, {}, { version: 'awg1.5' })).base).toBe(55);
		expect(scoreConfig(base({ h1: '10-2000', h2: '3000-4000', h3: '5000-6000', h4: '7000-8000' }, {}, { version: 'awg2.0' })).base).toBe(65);
	});

	it('awg3 с header protection = 85, awg3.1 с HP и trailers = 95', () => {
		const hp = base({ headerProtection: HP_KEY_OK, s1: 12, s2: 12, s3: 12, s4: 12 }, {}, { version: 'awg3' });
		expect(scoreConfig(hp).base).toBe(85);
		const rt = base({ headerProtection: HP_KEY_OK, s1: 12, s2: 12, s3: 12, s4: 12, randomTrailers: true }, {}, { version: 'awg3.1' });
		expect(scoreConfig(rt).base).toBe(95);
	});

	it('awg3 без HP (только таймеры) считается по нижележащей обфускации', () => {
		const timers = base({ rekeyAfterTime: '120-150' }, {}, { version: 'awg3' });
		expect(scoreConfig(timers).base).toBe(40);
		expect(scoreConfig(timers).facts.headerProtection).toBe(false);
		const timersOnWg = base({ rekeyAfterTime: '120-150', jc: 0, h1: '', h2: '', h3: '', h4: '' }, {}, { version: 'awg3' });
		expect(scoreConfig(timersOnWg).base).toBe(0);
	});
});

describe('scoreConfig — заголовки', () => {
	it('H = 1-4 без HP: fail −25', () => {
		const r = scoreConfig(base({ h1: '1', h2: '2', h3: '3', h4: '4' }));
		expect(find(r, 'H1–H4').status).toBe('fail');
		expect(find(r, 'H1–H4').delta).toBe(-25);
	});

	it('H = 1-4 при HP: info без штрафа (заголовок зашифрован)', () => {
		const r = scoreConfig(base({ h1: '1', h2: '2', h3: '3', h4: '4', headerProtection: true, s1: 12, s2: 12, s3: 12, s4: 12 }, {}, { version: 'awg3' }));
		expect(find(r, 'H1–H4').status).toBe('info');
		expect(find(r, 'H1–H4').delta).toBe(0);
		expect(r.score).toBe(85);
	});

	it('H < 5 без диапазонов: warn −5; узкий диапазон < 1000: warn −5', () => {
		expect(find(scoreConfig(base({ h1: '3', h2: '20', h3: '30', h4: '40' })), 'H1–H4').delta).toBe(-5);
		const narrow = base({ h1: '10-500', h2: '3000-4000', h3: '5000-6000', h4: '7000-8000' }, {}, { version: 'awg2.0' });
		expect(find(scoreConfig(narrow), 'H1–H4').delta).toBe(-5);
	});

	// Незаданный H модуль берёт из дефолта устройства 1/2/3/4 — оценка считает так же.
	it('H не заданы без HP = дефолт 1/2/3/4: fail −25', () => {
		const r = scoreConfig(base({ h1: '', h2: '', h3: '', h4: '' }));
		expect(find(r, 'H1–H4').status).toBe('fail');
		expect(find(r, 'H1–H4').delta).toBe(-25);
		expect(find(r, 'H1–H4').value).toBe('— / — / — / —');
	});

	it('задан только H1 = 10, остальные дефолтные 2/3/4: warn −5', () => {
		expect(find(scoreConfig(base({ h1: '10', h2: '', h3: '', h4: '' })), 'H1–H4').delta).toBe(-5);
	});
});

describe('scoreConfig — junk и паддинг', () => {
	it('Jc = 0 без HP: fail −10; Jc > 10: warn −5; Jmax−Jmin < 30: warn −5', () => {
		expect(find(scoreConfig(base({ jc: 0 })), 'Jc').delta).toBe(-10);
		expect(find(scoreConfig(base({ jc: 20 })), 'Jc').delta).toBe(-5);
		expect(find(scoreConfig(base({ jmin: 50, jmax: 60 })), 'Jmin/Jmax').delta).toBe(-5);
	});

	it('S1 = 0 и S2 = 0 без HP: −5 каждый; S1+56 = S2: fail −10', () => {
		const r = scoreConfig(base({ s1: 0, s2: 0 }));
		expect(find(r, 'S1').delta).toBe(-5);
		expect(find(r, 'S2').delta).toBe(-5);
		expect(find(scoreConfig(base({ s1: 10, s2: 66 })), 'S1 + 56 ≠ S2').delta).toBe(-10);
	});

	it('чистый WG: Jc и S1/S2 — info без штрафа (база 0, штрафовать нечего)', () => {
		const r = scoreConfig(base({ jc: 0, s1: 0, s2: 0, h1: '1', h2: '2', h3: '3', h4: '4' }, {}, { version: 'wg' }));
		expect(find(r, 'Jc').status).toBe('info');
		expect(find(r, 'S1').status).toBe('info');
		expect(find(r, 'S2').status).toBe('info');
		expect(r.score).toBe(0);
	});

	it('AWG 3.1 с HP: S1=S2=S3=S4 ≥ 12 — pass с заметкой о скорости, иначе −15', () => {
		const ok = base({ headerProtection: true, randomTrailers: true, s1: 12, s2: 12, s3: 12, s4: 12 }, {}, { version: 'awg3.1' });
		const c = find(scoreConfig(ok), 'S1 = S2 = S3 = S4 ≥ 12');
		expect(c.status).toBe('pass');
		expect(c.detail).toMatch(/скорост/);
		const bad = base({ headerProtection: true, randomTrailers: true, s1: 12, s2: 14, s3: 12, s4: 12 }, {}, { version: 'awg3.1' });
		expect(find(scoreConfig(bad), 'S1 = S2 = S3 = S4 ≥ 12').delta).toBe(-15);
		// На 3.0 правила нет.
		const v30 = base({ headerProtection: true, s1: 12, s2: 14, s3: 12, s4: 12 }, {}, { version: 'awg3' });
		expect(scoreConfig(v30).checks.some((x) => x.title === 'S1 = S2 = S3 = S4 ≥ 12')).toBe(false);
		// Без HP (только RandomTrailers) правила нет.
		const noHp = base({ randomTrailers: true, s1: 12, s2: 14, s3: 12, s4: 12 }, {}, { version: 'awg3.1' });
		expect(scoreConfig(noHp).checks.some((x) => x.title === 'S1 = S2 = S3 = S4 ≥ 12')).toBe(false);
		// Бэкенд уже вернул hp_padding_min за те же значения — второго штрафа нет.
		const dup = base({ headerProtection: true, randomTrailers: true, s1: 12, s2: 5, s3: 12, s4: 12 }, {}, { version: 'awg3.1', errors: [{ code: 'hp_padding_min', message: 'S2 = 5: …' }] });
		expect(scoreConfig(dup).checks.some((x) => x.title === 'S1 = S2 = S3 = S4 ≥ 12')).toBe(false);
	});

	it('при HP штраф S1+56=S2 не начисляется — значения не видны на проводе', () => {
		const r = scoreConfig(base({ headerProtection: true, s1: 12, s2: 68, s3: 12, s4: 12 }, {}, { version: 'awg3' }));
		expect(r.checks.some((x) => x.title === 'S1 + 56 ≠ S2')).toBe(false);
		expect(r.score).toBe(85);
	});

	it('при HP Jmin/Jmax — info без штрафа', () => {
		const r = scoreConfig(base({ headerProtection: true, jc: 3, jmin: 10, jmax: 30, s1: 12, s2: 12, s3: 12, s4: 12 }, {}, { version: 'awg3' }));
		const c = find(r, 'Jmin/Jmax');
		expect(c.status).toBe('info');
		expect(c.delta).toBe(0);
		expect(r.score).toBe(85);
	});
});

describe('scoreConfig — CPS', () => {
	it('I2–I5 не дают бонуса: профили генератора заполняют только I1 (SIP — I1 и I2)', () => {
		const r = scoreConfig(base({ headerProtection: true, randomTrailers: true, s1: 12, s2: 12, s3: 12, s4: 12, i1: '<b 0xc0000001>', i2: '<b 0x01>', i3: '<b 0x02>', i4: '<b 0x03>', i5: '<b 0x04>' }, {}, { version: 'awg3.1' }));
		expect(find(r, 'Цепочка I2–I5')).toBeUndefined();
		const one = scoreConfig(base({ headerProtection: true, randomTrailers: true, s1: 12, s2: 12, s3: 12, s4: 12, i1: '<b 0xc0000001>' }, {}, { version: 'awg3.1' }));
		expect(r.score).toBe(one.score);
	});

	it('I1 с ошибкой структуры тегов: fail −10; протокол попадает в факты', () => {
		const good = scoreConfig(base({ i1: '<b 0xc000000001><r 16>' }, {}, { version: 'awg1.5' }));
		expect(good.facts.cps).toBe('QUIC');
		const bad = scoreConfig(base({ i1: '<r 16><b 0xc000000001>' }, {}, { version: 'awg1.5' }));
		expect(find(bad, 'Структура I1').delta).toBe(-10);
	});

	it('любой тег делает I1 тегированным: без <b> первым — fail −10', () => {
		const r = scoreConfig(base({ i1: '<r 16><t>' }, {}, { version: 'awg1.5' }));
		const c = find(r, 'Структура I1');
		expect(c.status).toBe('fail');
		expect(c.delta).toBe(-10);
	});
});

describe('scoreConfig — сервер, сеть, факты', () => {
	it('порт 51820: warn −5', () => {
		expect(find(scoreConfig(base({}, { endpoint: 'vpn.example.com:51820' })), 'Порт Endpoint').delta).toBe(-5);
	});

	it('MTU: потолок 1440 для IPv4-endpoint, 1420 для IPv6; ContentPadding потолок не снижает; без баллов', () => {
		expect(find(scoreConfig(base({ mtu: 1440 })), 'MTU').status).toBe('pass');
		expect(find(scoreConfig(base({ mtu: 1441 })), 'MTU').status).toBe('warn');
		expect(find(scoreConfig(base({ mtu: 1421 }, { endpoint: '[2001:db8::1]:4443' })), 'MTU').status).toBe('warn');
		// Движок режет добавку по свободному месту в UDP-окне — потолок прежний.
		const padded = find(scoreConfig(base({ mtu: 1400, contentPaddingAddition: '0-64' })), 'MTU');
		expect(padded.status).toBe('pass');
		expect(padded.detail).toMatch(/ContentPaddingAddition 0-64 обрезается движком/);
		expect(find(scoreConfig(base({ mtu: 1501 })), 'MTU').status).toBe('fail');
		expect(find(scoreConfig(base({ mtu: 1279 })), 'MTU').status).toBe('fail');
		// Парсерный дефолт 1280 без ключа в тексте — info, не pass.
		const unset = find(scoreConfig(base({ mtu: 1280, mtuSet: false })), 'MTU');
		expect(unset.status).toBe('info');
		expect(unset.value).toBe('не задан');
		expect(find(scoreConfig(base({ mtu: 1441 })), 'MTU').delta).toBe(0);
	});

	it('PSK — факт без баллов; из туннеля подписывается отдельно', () => {
		const c = find(scoreConfig(base({}, { hasPresharedKey: true })), 'PresharedKey');
		expect(c.status).toBe('info');
		expect(c.value).toBe('задан');
		expect(c.delta).toBe(0);
		expect(find(scoreConfig(base({}, { hasPresharedKey: true, presharedKeyFromStore: true })), 'PresharedKey').value).toBe('задан (из туннеля)');
	});

	it('дефолты парсера в сводке помечены «по умолчанию»', () => {
		const r = scoreConfig(base({ mtu: 1280, mtuSet: false }, { persistentKeepalive: '25', keepaliveSet: false, allowedIPs: ['0.0.0.0/0', '::/0'], allowedIPsSet: false }));
		expect(r.summary.find((s) => s.label === 'MTU')?.value).toBe('не задан (по умолчанию 1280)');
		expect(r.summary.find((s) => s.label === 'PersistentKeepalive')?.value).toBe('25 (по умолчанию)');
		expect(r.summary.find((s) => s.label === 'AllowedIPs')?.value).toBe('0.0.0.0/0, ::/0 (по умолчанию)');
	});

	it('ошибки совместимости → вердикт «не поднимется», в балл не входят и не дублируются в рекомендациях', () => {
		const r = scoreConfig(base({}, {}, { errors: [{ code: 'hp_padding_min', message: 'S2 = 5: …' }] }));
		expect(r.verdict.label).toBe('Конфиг не поднимется');
		expect(r.verdict.tone).toBe('error');
		expect(r.verdict.text).toBe('Бэкенд отверг конфиг, ошибки ниже.');
		expect(r.score).toBe(40);
		expect(r.checks.find((c) => c.cat === 'Совместимость')?.status).toBe('fail');
		// Сообщение бэкенда показывают блок ошибок и чек — в «Рекомендации» оно не идёт.
		expect(buildFixes(r.checks).some((f) => f.includes('S2 = 5'))).toBe(false);
	});

	it('awg3 без header protection: в тексте вердикта — пометка о профиле', () => {
		const r = scoreConfig(base({ rekeyAfterTime: '120-150' }, {}, { version: 'awg3' }));
		expect(r.verdict.text).toContain('(без header protection)');
	});

	it('вердикт по порогам и факты', () => {
		const r = scoreConfig(base({ headerProtection: true, randomTrailers: true, s1: 12, s2: 12, s3: 12, s4: 12 }, {}, { version: 'awg3.1' }));
		expect(r.verdict.label).toBe('Сильная обфускация');
		expect(r.facts).toEqual({ profile: 'AWG 3.1', headerProtection: true, cps: 'нет', trailers: true });
		expect(scoreConfig(base({}, {}, { version: 'wg', interface: { ...base().interface, jc: 0, h1: '1', h2: '2', h3: '3', h4: '4' } })).verdict.label).toBe('Минимальная или нет обфускации');
	});

	it('keepalive-диапазон уходит в сводку как есть', () => {
		const r = scoreConfig(base({}, { persistentKeepalive: '25-35' }));
		expect(r.summary.find((s) => s.label === 'PersistentKeepalive')?.value).toBe('25-35');
	});
});

describe('buildFixes', () => {
	it('одна строка на проваленный чек с fix', () => {
		const r = scoreConfig(base({ h1: '1', h2: '2', h3: '3', h4: '4', jc: 0 }));
		const fixes = buildFixes(r.checks);
		expect(fixes.length).toBeGreaterThanOrEqual(2);
		expect(fixes.some((f) => /H1/.test(f))).toBe(true);
	});
});
