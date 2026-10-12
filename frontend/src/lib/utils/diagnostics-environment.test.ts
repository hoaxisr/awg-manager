import { describe, expect, it, vi } from 'vitest';

// Модуль тянет стор темы, который при импорте читает prefers-color-scheme.
vi.hoisted(() => {
	Object.defineProperty(window, 'matchMedia', {
		configurable: true,
		value: (query: string) => ({
			matches: false,
			media: query,
			addEventListener: () => {},
			removeEventListener: () => {},
			addListener: () => {},
			removeListener: () => {},
		}),
	});
});
import { routerClientRows, type RouterClientContext } from './about-device';
import { reportClientRows, sanitizeClientRows } from './diagnostics-environment';

const MAC = 'aa:bb:cc:dd:ee:ff';
const HOSTNAME = 'secret-laptop';
const NDMS_NAME = 'Секретный ноутбук';

const ctx: RouterClientContext = {
	clientIP: '192.168.1.42',
	hostname: HOSTNAME,
	policyMessage: '',
	fromRouter: true,
	device: {
		mac: MAC,
		ip: '192.168.1.42',
		name: NDMS_NAME,
		hostname: HOSTNAME,
		active: true,
		link: 'up',
		policy: '',
	},
};

function leaked(rows: { value: string; title?: string }[]): string[] {
	const text = rows.map((r) => `${r.value} ${r.title ?? ''}`).join('\n');
	return [MAC, HOSTNAME, NDMS_NAME].filter((s) => text.includes(s));
}

describe('маскирование клиента в отчёте окружения', () => {
	// Строки берутся из настоящего построителя about-device.ts: если там
	// переименуют id строк MAC / hostname / имени в NDMS, этот тест упадёт,
	// а не пропустит их в сохраняемый отчёт.
	it('sanitizeClientRows прячет MAC, hostname и имя в NDMS по id строк', () => {
		const raw = routerClientRows(ctx);
		expect(leaked(raw)).toEqual([MAC, HOSTNAME, NDMS_NAME]);
		expect(leaked(sanitizeClientRows(raw))).toEqual([]);
	});

	it('строки отчёта целиком не содержат исходных значений', () => {
		expect(leaked(reportClientRows(ctx))).toEqual([]);
	});
});
