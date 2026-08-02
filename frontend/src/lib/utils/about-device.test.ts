import { describe, it, expect } from 'vitest';
import { buildAwgmServicesSnapshot, awgmServicesRows } from './about-device';

// #663: прокси (SOCKS5/HTTP) и client routes — разные строки отчёта.
describe('awgmServicesRows', () => {
	const snap = buildAwgmServicesSnapshot({
		level: 'advanced',
		theme: null,
		settings: null,
		authDisabled: true,
		authenticated: false,
		login: null,
		singbox: null,
		hydra: null,
		deviceProxy: { enabled: false, port: 1080, selectedOutbound: '' } as never,
		deviceProxyRuntime: null,
		clientRoutesTotal: 1,
		clientRoutesEnabled: 1,
		clientRoutesLoaded: true,
		dnsRoutesTotal: 0,
		dnsRoutesEnabled: 0,
		awgRunning: 0,
		awgTotal: 0,
		subscriptionsEnabled: 0,
		subscriptionsTotal: 0,
	});

	const value = (label: string) => awgmServicesRows(snap).find((r) => r.label === label)?.value;

	it('показывает правила client routes под «VPN для устройств»', () => {
		expect(value('VPN для устройств')).toBe('1 вкл / 1 всего');
	});

	it('показывает прокси отдельной строкой', () => {
		expect(value('Прокси для устройств')).toBe('выкл');
	});
});
