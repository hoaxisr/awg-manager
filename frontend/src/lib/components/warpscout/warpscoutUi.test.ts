import { describe, expect, it } from 'vitest';
import type { WarpConfig, WarpHit, WarpProfile, WarpRouteRef } from '$lib/types/warpscout';
import {
	LIMITS,
	SPARK_SLOTS,
	buildSparkBars,
	canApplyFindJunk,
	countryFlag,
	defaultFilters,
	findTunnelIpv4Conflict,
	formatElapsed,
	formatSlaPct,
	formatUptime,
	hasUsableFindJunkBest,
	humanizeSwapReason,
	isValidTunnelIpv4,
	latencyTone,
	parseGoTime,
	parseTunnelIpv4,
	routeChipText,
	routeDisplayLabel,
	sampleBarHeight,
	sanitizeConfig,
	sanitizeInt,
	sectionEqual,
	setProfileEnabled,
	setProfileMode,
	sortHits,
	suggestFreeTunnelIpv4,
	syncDraft,
	targetSelectable,
	topColos,
	warpErrorMessage,
	watchStateView,
} from './warpscoutUi';

function profile(id: string, over: Partial<WarpProfile> = {}): WarpProfile {
	return {
		id,
		name: id,
		enabled: false,
		targetMode: 'kernel',
		targetId: `t-${id}`,
		filters: defaultFilters(0),
		checkIntervalSec: 60,
		failThreshold: 2,
		cooldownSec: 120,
		...over,
	};
}

function config(over: Partial<WarpConfig> = {}): WarpConfig {
	return {
		profileMode: 'single',
		profiles: [],
		awg: { signatureProfile: 'quic_initial', jc: 4, jmin: 40, jmax: 70 },
		...over,
	};
}

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
		expect(formatUptime('2026-11-12T09:59:15Z', baseMs)).toBe('45 сек');
		expect(formatUptime('2026-11-12T09:40:00Z', baseMs)).toBe('20 мин');
		expect(formatUptime('2026-11-12T06:30:00Z', baseMs)).toBe('3 ч 30 мин');
		expect(formatUptime('2026-11-10T04:00:00Z', baseMs)).toBe('2 д 6 ч');
		expect(formatElapsed(45_400)).toBe('45 с');
		expect(formatElapsed(125_000)).toBe('2 мин 5 с');

		const colos = topColos({ HEL: 15, ARN: 5, WAW: 5 }, 2);
		expect(colos).toEqual([
			{ colo: 'HEL', count: 15, pct: 60 },
			{ colo: 'ARN', count: 5, pct: 20 },
		]);
	});

	it('sanitizeInt clamps to the field limits and falls back to the default', () => {
		expect(sanitizeInt(5, LIMITS.checkIntervalSec)).toBe(15);
		expect(sanitizeInt(999999, LIMITS.checkIntervalSec)).toBe(86400);
		expect(sanitizeInt('90', LIMITS.checkIntervalSec)).toBe(90);
		expect(sanitizeInt(undefined, LIMITS.checkIntervalSec)).toBe(60);
		expect(sanitizeInt('', LIMITS.failThreshold)).toBe(2);
		expect(sanitizeInt('abc', LIMITS.failThreshold)).toBe(2);
		// Бэкенд превращает паузу <= 0 в 120: ноль в поле не должен молча стать двумя минутами.
		expect(LIMITS.cooldownSec.min).toBe(1);
		expect(sanitizeInt(0, LIMITS.cooldownSec)).toBe(1);
	});

	it('sanitizeConfig clamps profile numbers and drops an empty active profile', () => {
		const out = sanitizeConfig(
			config({
				activeProfileId: 'gone',
				scanInterface: '  ',
				profiles: [profile('a', { checkIntervalSec: 1, failThreshold: 500, cooldownSec: 0 })],
			}),
		);
		expect(out.profiles[0]).toMatchObject({ checkIntervalSec: 15, failThreshold: 100, cooldownSec: 1 });
		expect(out.scanInterface).toBeUndefined();
		expect(out.activeProfileId).toBeUndefined();
		expect('activeProfileId' in JSON.parse(JSON.stringify(out))).toBe(false);

		const active = sanitizeConfig(config({ profiles: [profile('a'), profile('b', { enabled: true })] }));
		expect(active.activeProfileId).toBe('b');
	});

	it('sectionEqual treats an omitted and an empty activeProfileId as the same', () => {
		const server = config({ profiles: [profile('a')] });
		expect(sectionEqual({ ...server, activeProfileId: '' }, server, 'profiles')).toBe(true);
		expect(sectionEqual({ ...server, activeProfileId: undefined }, server, 'profiles')).toBe(true);
		expect(sectionEqual({ ...server, activeProfileId: 'a' }, server, 'profiles')).toBe(false);
		// Ключ, дописанный в конец объекта, порядок сравнения не ломает.
		const reordered = { profiles: server.profiles, awg: server.awg, profileMode: server.profileMode } as WarpConfig;
		expect(sectionEqual(reordered, server, 'profiles')).toBe(true);
		const awgEdited = { ...server, awg: { ...server.awg, jc: 5 } };
		expect(sectionEqual(awgEdited, server, 'awg')).toBe(false);
		expect(sectionEqual(awgEdited, server, 'profiles')).toBe(true);
	});

	it('sectionEqual ignores server-generated I1..I5 of a named preset', () => {
		const server = config({ profiles: [profile('a')] });
		const saved = { ...server, awg: { ...server.awg, signatureProfile: 'dns', i1: '<b 0xdead>' } };
		// Выбрали другой пресет и вернулись: черновик без пакетов, пресет прежний.
		const draft = { ...server, awg: { ...server.awg, signatureProfile: 'dns' } };
		expect(sectionEqual(draft, saved, 'awg')).toBe(true);
		expect(sectionEqual({ ...draft, awg: { ...draft.awg, signatureProfile: 'stun' } }, saved, 'awg')).toBe(false);
		// У «custom» пакеты задаёт пользователь — они сравниваются.
		const custom = { ...server, awg: { ...server.awg, signatureProfile: 'custom', i1: '<b 0xdead>' } };
		const customEdited = { ...server, awg: { ...server.awg, signatureProfile: 'custom', i1: '<b 0xbeef>' } };
		expect(sectionEqual(customEdited, custom, 'awg')).toBe(false);
	});

	it('syncDraft takes untouched sections from the server and keeps edited ones', () => {
		const prev = config({ profiles: [profile('a')] });
		const next = config({ profiles: [profile('a'), profile('b')], awg: { ...prev.awg, jc: 9 } });

		// Первая загрузка — черновик целиком с сервера.
		expect(syncDraft(config(), null, next)).toEqual(next);

		// Правок нет — обе части с сервера.
		const clean = syncDraft(structuredClone(prev), prev, next);
		expect(clean.profiles.map((p) => p.id)).toEqual(['a', 'b']);
		expect(clean.awg.jc).toBe(9);

		// Правки в профилях: они остаются, AWG всё равно обновляется.
		const edited = structuredClone(prev);
		edited.profiles[0].name = 'edited';
		const kept = syncDraft(edited, prev, next);
		expect(kept.profiles.map((p) => p.name)).toEqual(['edited']);
		expect(kept.awg.jc).toBe(9);

		// force — часть только что сохранена: берётся с сервера даже с правками.
		expect(syncDraft(edited, prev, next, ['profiles']).profiles.map((p) => p.id)).toEqual(['a', 'b']);

		// Правки в AWG не мешают принять новые профили (и наоборот).
		const awgEdited = structuredClone(prev);
		awgEdited.awg.jmax = 99;
		const mixed = syncDraft(awgEdited, prev, next);
		expect(mixed.awg.jmax).toBe(99);
		expect(mixed.profiles.map((p) => p.id)).toEqual(['a', 'b']);

		// Сервер не изменился — возвращается тот же объект, без лишней перерисовки.
		expect(syncDraft(edited, prev, structuredClone(prev))).toBe(edited);
	});

	it('setProfileEnabled keeps a single active profile and never writes an empty id', () => {
		const single = config({ profiles: [profile('a', { enabled: true }), profile('b')], activeProfileId: 'a' });
		setProfileEnabled(single, 'b', true);
		expect(single.profiles.map((p) => p.enabled)).toEqual([false, true]);
		expect(single.activeProfileId).toBe('b');
		setProfileEnabled(single, 'b', false);
		expect(single.profiles.map((p) => p.enabled)).toEqual([false, false]);
		expect(single.activeProfileId).toBeUndefined();

		const multi = config({ profileMode: 'multi', profiles: [profile('a', { enabled: true }), profile('b')] });
		setProfileEnabled(multi, 'b', true);
		expect(multi.profiles.map((p) => p.enabled)).toEqual([true, true]);
		expect(multi.activeProfileId).toBeUndefined();
	});

	it('setProfileMode collapses to the active or the first enabled profile', () => {
		const c = config({
			profileMode: 'multi',
			activeProfileId: 'b',
			profiles: [profile('a', { enabled: true }), profile('b', { enabled: true })],
		});
		setProfileMode(c, 'single');
		expect(c.profiles.map((p) => p.enabled)).toEqual([false, true]);
		expect(c.activeProfileId).toBe('b');

		const none = config({ profileMode: 'multi', activeProfileId: 'x', profiles: [profile('a')] });
		setProfileMode(none, 'single');
		expect(none.activeProfileId).toBeUndefined();

		setProfileMode(c, 'multi');
		expect(c.profileMode).toBe('multi');
		expect(c.profiles.map((p) => p.enabled)).toEqual([false, true]);
	});

	it('buildSparkBars pads on the left and keeps the newest samples', () => {
		expect(SPARK_SLOTS).toBe(20);
		const samples = [1, 2, 3].map((n) => ({ at: `2026-11-12T10:00:0${n}Z`, ok: true, latencyMs: n }));
		expect(buildSparkBars(samples, 5).map((s) => s?.latencyMs ?? null)).toEqual([null, null, 1, 2, 3]);
		expect(buildSparkBars(samples, 2).map((s) => s?.latencyMs)).toEqual([2, 3]);
		expect(buildSparkBars(undefined, 2)).toEqual([null, null]);
	});

	it('sampleBarHeight uses perceptual scaling and marks failures as full bars', () => {
		expect(sampleBarHeight({ at: '', ok: false })).toBe(100);
		expect(sampleBarHeight({ at: '', ok: true })).toBe(52);
		expect(sampleBarHeight({ at: '', ok: true, latencyMs: 15 })).toBe(42);
		expect(sampleBarHeight({ at: '', ok: true, latencyMs: 38 })).toBeGreaterThanOrEqual(44);
		expect(sampleBarHeight({ at: '', ok: true, latencyMs: 300 })).toBe(82);
		expect(sampleBarHeight({ at: '', ok: true, latencyMs: 3065 })).toBe(96);
	});

	it('validates tunnel IPv4, detects conflicts and suggests a free address', () => {
		expect(parseTunnelIpv4(' 172.16.0.4/32 ')?.normalized).toBe('172.16.0.4');
		expect(parseTunnelIpv4('10.66.66.2/24')?.normalized).toBe('10.66.66.2/24');
		expect(isValidTunnelIpv4('127.0.0.1')).toBe(false);
		expect(isValidTunnelIpv4('169.254.1.10')).toBe(false);
		expect(isValidTunnelIpv4('2001:db8::1')).toBe(false);

		const targets = [
			{ mode: 'kernel' as const, id: 't1', name: 'WARP HEL', iface: 'nwg3', endpoint: '', addressV4: '172.16.0.2', isWarp: true, running: true, protected: false },
		];
		expect(findTunnelIpv4Conflict('172.16.0.2', targets, [])).toEqual({
			conflict: true,
			host: '172.16.0.2',
			tunnelName: 'WARP HEL',
		});
		expect(findTunnelIpv4Conflict('172.16.0.3', targets, ['172.16.0.3'])).toEqual({
			conflict: true,
			host: '172.16.0.3',
		});
		expect(suggestFreeTunnelIpv4('172.16.0.2', targets, ['172.16.0.3'])).toBe('172.16.0.4');
	});

	it('sortHits orders by ping and puts unmeasured endpoints last', () => {
		const hit = (endpoint: string, pingMs: number, handshakeMs: number): WarpHit => ({
			endpoint,
			pingMs,
			handshakeMs,
			colo: '',
			loc: '',
			warp: '',
			tgOk: false,
		});
		const sorted = sortHits([hit('none', 0, 1), hit('slow', 90, 5), hit('fast-b', 20, 9), hit('fast-a', 20, 3)]);
		expect(sorted.map((h) => h.endpoint)).toEqual(['fast-a', 'fast-b', 'slow', 'none']);
		expect(sortHits(null)).toEqual([]);
	});

	it('route labels resolve the default WAN and named interfaces', () => {
		const routes: WarpRouteRef[] = [
			{ name: 'ppp0', label: 'Provider', kind: 'wan', up: true },
			{ name: 'nwg1', label: 'nwg1', kind: 'tunnel', up: true },
		];
		expect(routeChipText(routes, 'ppp0', '')).toBe('WAN · Provider (ppp0)');
		expect(routeChipText(routes, 'eth3', '')).toBe('WAN · eth3');
		expect(routeChipText(routes, 'ppp0', 'nwg1')).toBe('nwg1');
		expect(routeChipText(routes, 'ppp0', 'gone0')).toBe('gone0');
		expect(routeDisplayLabel(routes, 'ppp0', 'ppp0')).toBe('Provider (ppp0)');
		expect(routeDisplayLabel(routes, 'ppp0', '')).toContain('Provider');
		expect(routeDisplayLabel(routes, '', '')).not.toBe('');
	});

	it('hasUsableFindJunkBest and canApplyFindJunk reject running, 0/N and incomplete bests', () => {
		const baseAttempt = {
			attempt: 1,
			awg: { signatureProfile: 'quic_initial', jc: 5, jmin: 30, jmax: 80, i1: '<b 0x11>' },
			profile: 'quic_initial',
			working: 12,
			scanned: 15,
			total: 15,
			pct: 80,
			durationMs: 2500,
		};
		expect(hasUsableFindJunkBest(null)).toBe(false);
		expect(canApplyFindJunk(null)).toBe(false);
		expect(
			hasUsableFindJunkBest({
				running: false,
				attempt: 1,
				maxAttempts: 5,
				thresholdPct: 95,
				attempts: [{ ...baseAttempt, working: 0, pct: 0 }],
				best: { ...baseAttempt, working: 0, pct: 0 },
			}),
		).toBe(false);
		expect(
			hasUsableFindJunkBest({
				running: false,
				attempt: 1,
				maxAttempts: 5,
				thresholdPct: 95,
				attempts: [{ ...baseAttempt, incomplete: true }],
				best: { ...baseAttempt, incomplete: true },
			}),
		).toBe(false);
		expect(
			canApplyFindJunk({
				running: true,
				attempt: 1,
				maxAttempts: 5,
				thresholdPct: 95,
				attempts: [baseAttempt],
				best: baseAttempt,
			}),
		).toBe(false);
		expect(
			canApplyFindJunk({
				running: false,
				attempt: 1,
				maxAttempts: 5,
				thresholdPct: 95,
				attempts: [baseAttempt],
				best: baseAttempt,
			}),
		).toBe(true);
	});
});
