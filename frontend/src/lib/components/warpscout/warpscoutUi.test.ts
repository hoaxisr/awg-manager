import { describe, expect, it } from 'vitest';
import {
	countryFlag,
	formatSlaPct,
	formatUptime,
	humanizeSwapReason,
	latencyTone,
	parseGoTime,
	targetSelectable,
	topColos,
	warpErrorMessage,
	watchStateView,
} from './warpscoutUi';

describe('warpscoutUi', () => {
	it('countryFlag builds regional-indicator pairs and ignores junk', () => {
		expect(countryFlag('fi')).toBe('🇫🇮');
		expect(countryFlag('DE')).toBe('🇩🇪');
		expect(countryFlag('XYZ')).toBe('');
		expect(countryFlag('')).toBe('');
		expect(countryFlag(undefined)).toBe('');
	});

	it('parseGoTime treats Go zero time as absent', () => {
		expect(parseGoTime('0001-01-01T00:00:00Z')).toBeNull();
		expect(parseGoTime('')).toBeNull();
		expect(parseGoTime('2026-11-11T10:00:00Z')?.toISOString()).toBe('2026-11-11T10:00:00.000Z');
	});

	it('watchStateView maps every backend state to a localized label', () => {
		const states = [
			'idle',
			'ok',
			'degraded',
			'swapping',
			'backoff',
			'wan_down',
			'target_missing',
			'target_stopped',
			'no_account',
			'engine_missing',
			'protected',
			'not_warp',
			'engine_busy',
		];
		for (const s of states) {
			const v = watchStateView(s);
			expect(v.label).not.toBe(s);
		}
		expect(watchStateView('ok').variant).toBe('success');
		expect(watchStateView('ok').tone).toBe('ok');
		expect(watchStateView('weird').label).toBe('weird');
	});

	it('warpErrorMessage uses codes and keeps INVALID_INPUT message', () => {
		const invalid = Object.assign(new Error('профиль «x» — не выбран туннель'), { code: 'INVALID_INPUT' });
		expect(warpErrorMessage(invalid)).toBe('профиль «x» — не выбран туннель');
		const busy = Object.assign(new Error('scan running'), { code: 'WARP_BUSY' });
		expect(warpErrorMessage(busy)).toContain('Сканер занят');
		expect(warpErrorMessage(busy)).toContain('scan running');
		expect(warpErrorMessage(new Error('boom'))).toBe('boom');
	});

	it('targetSelectable requires WARP key and no protection', () => {
		const base = { mode: 'kernel' as const, id: 'a', name: 'a', iface: '', endpoint: '', running: true };
		expect(targetSelectable({ ...base, isWarp: true, protected: false })).toBe(true);
		expect(targetSelectable({ ...base, isWarp: false, protected: false })).toBe(false);
		expect(targetSelectable({ ...base, isWarp: true, protected: true })).toBe(false);
	});

	it('humanizeSwapReason condenses Go transport errors and preserves domain reasons', () => {
		expect(
			humanizeSwapReason(
				'нет связи через туннель: httpclient: request failed: Get "http://cp.cloudflare.com/cdn-cgi/trace": dial tcp4 104.16.133.229:80: i/o timeout',
			),
		).toBe('Таймаут проверки trace');
		expect(humanizeSwapReason('нет связи через туннель: read tcp: connection reset by peer')).toBe(
			'Обрыв соединения в туннеле',
		);
		expect(humanizeSwapReason('узел переехал в запрещённый колокейшн DME')).toBe(
			'узел переехал в запрещённый колокейшн DME',
		);
		expect(latencyTone(25)).toBe('ok');
		expect(latencyTone(95)).toBe('slow');
		expect(latencyTone(220)).toBe('fail');
		expect(latencyTone(0)).toBe('none');
	});

	it('formats autopilot statistics helpers accurately', () => {
		expect(formatSlaPct(0, 0)).toBe('—');
		expect(formatSlaPct(100, 100)).toBe('100%');
		expect(formatSlaPct(199, 200)).toBe('99.5%');
		expect(formatSlaPct(85, 100)).toBe('85%');

		const baseMs = Date.parse('2026-11-12T10:00:00Z');
		expect(formatUptime('0001-01-01T00:00:00Z', baseMs)).toBe('—');
		expect(formatUptime('2026-11-12T09:59:15Z', baseMs)).toBe('45 с');
		expect(formatUptime('2026-11-12T09:40:00Z', baseMs)).toBe('20 мин');
		expect(formatUptime('2026-11-12T06:30:00Z', baseMs)).toBe('3 ч 30 мин');
		expect(formatUptime('2026-11-10T04:00:00Z', baseMs)).toBe('2 д 6 ч');

		const colos = topColos({ HEL: 15, ARN: 5, WAW: 5 }, 2);
		expect(colos).toEqual([
			{ colo: 'HEL', count: 15, pct: 60 },
			{ colo: 'ARN', count: 5, pct: 20 },
		]);
	});
});
