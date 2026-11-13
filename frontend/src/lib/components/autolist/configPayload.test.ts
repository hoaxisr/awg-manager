import { describe, expect, it } from 'vitest';
import {
	autolistConfigDraft,
	AUTOLIST_DEFAULT_ADDS_PER_HOUR,
	AUTOLIST_DEFAULT_WAN_URL,
	AUTOLIST_MAX_ADDS_PER_HOUR,
	buildAutolistConfigPayload,
	mergeAutolistExclusions,
	normalizeAutolistAddsPerHour,
} from './configPayload';

describe('buildAutolistConfigPayload', () => {
	const fullCfg = {
		autoAdd: false,
		targetInterface: '',
		resolveVia: '',
		wanCheckUrl: AUTOLIST_DEFAULT_WAN_URL,
		wanMonitorEnabled: false,
		tunnelProbe: false,
		excludeDomains: [] as string[],
		excludeRuleSets: [] as string[],
		udpEnabled: false,
		verbose: false,
	};

	it('keeps cleared strings, explicit false values, and empty arrays in the JSON body', () => {
		const payload = buildAutolistConfigPayload({
			autoAdd: false,
			targetInterface: '',
			resolveVia: '',
			wanCheckUrl: '',
			wanMonitorEnabled: false,
			tunnelProbe: false,
			excludeDomains: [],
			excludeRuleSets: [],
			udpEnabled: false,
			verbose: false,
		});

		expect(JSON.parse(JSON.stringify(payload))).toEqual({
			autoAdd: false,
			targetInterface: '',
			resolveVia: '',
			wanCheckUrl: AUTOLIST_DEFAULT_WAN_URL,
			wanMonitorEnabled: false,
			tunnelProbe: false,
			excludeDomains: [],
			excludeRuleSets: [],
			udpEnabled: false,
			verbose: false,
			addsPerHour: AUTOLIST_DEFAULT_ADDS_PER_HOUR,
		});
	});

	it('sends exactly the eleven 0.9.0 settings — no legacy fields in the body', () => {
		const payload = buildAutolistConfigPayload({
			autoAdd: true,
			targetInterface: 'nwg0',
			resolveVia: 'sb:quad9',
			wanCheckUrl: 'https://ya.ru/robots.txt',
			wanMonitorEnabled: true,
			tunnelProbe: true,
			excludeDomains: ['example.com'],
			excludeRuleSets: ['geosite-google'],
			udpEnabled: true,
			verbose: true,
			addsPerHour: 25,
		}) as unknown as Record<string, unknown>;

		expect(Object.keys(payload).sort()).toEqual([
			'addsPerHour',
			'autoAdd',
			'excludeDomains',
			'excludeRuleSets',
			'resolveVia',
			'targetInterface',
			'tunnelProbe',
			'udpEnabled',
			'verbose',
			'wanCheckUrl',
			'wanMonitorEnabled',
		]);
		expect(payload.addsPerHour).toBe(25);
	});

	it('coerces a string addsPerHour from the number input and clamps garbage to the default', () => {
		// Поле Input всегда отдаёт строку, даже при type="number".
		expect(buildAutolistConfigPayload({ ...fullCfg, addsPerHour: '75' as unknown as number }).addsPerHour).toBe(75);
		expect(autolistConfigDraft({ addsPerHour: '75' as unknown as number }).addsPerHour).toBe(75);

		// Мусор и вне диапазона — значения по умолчанию, как у бэкенда (1..500).
		expect(normalizeAutolistAddsPerHour('')).toBe(AUTOLIST_DEFAULT_ADDS_PER_HOUR);
		expect(normalizeAutolistAddsPerHour('abc')).toBe(AUTOLIST_DEFAULT_ADDS_PER_HOUR);
		expect(normalizeAutolistAddsPerHour(0)).toBe(AUTOLIST_DEFAULT_ADDS_PER_HOUR);
		expect(normalizeAutolistAddsPerHour(501)).toBe(AUTOLIST_DEFAULT_ADDS_PER_HOUR);
		expect(normalizeAutolistAddsPerHour(null)).toBe(AUTOLIST_DEFAULT_ADDS_PER_HOUR);
		expect(normalizeAutolistAddsPerHour(undefined)).toBe(AUTOLIST_DEFAULT_ADDS_PER_HOUR);
		expect(normalizeAutolistAddsPerHour(12.5)).toBe(AUTOLIST_DEFAULT_ADDS_PER_HOUR);
		// Границы принимаются.
		expect(normalizeAutolistAddsPerHour(1)).toBe(1);
		expect(normalizeAutolistAddsPerHour(AUTOLIST_MAX_ADDS_PER_HOUR)).toBe(AUTOLIST_MAX_ADDS_PER_HOUR);
		expect(buildAutolistConfigPayload({ ...fullCfg, addsPerHour: 0 }).addsPerHour).toBe(AUTOLIST_DEFAULT_ADDS_PER_HOUR);
	});

	it('trims string fields and drops empty exclude lists to empty arrays', () => {
		const payload = buildAutolistConfigPayload({
			autoAdd: false,
			targetInterface: '  awg0  ',
			resolveVia: 'sb:dns-google',
			wanCheckUrl: '  https://ya.ru/robots.txt  ',
			wanMonitorEnabled: false,
			tunnelProbe: true,
			excludeDomains: ['a.com', 'b.com'],
			excludeRuleSets: [],
			udpEnabled: false,
			verbose: false,
		});

		expect(payload.targetInterface).toBe('awg0');
		expect(payload.wanCheckUrl).toBe('https://ya.ru/robots.txt');
		expect(payload.excludeRuleSets).toEqual([]);
	});
});

describe('autolistConfigDraft', () => {
	it('treats omitted booleans as their defaults without inventing an autoAdd=true', () => {
		const draft = autolistConfigDraft({ autoAdd: false });
		expect(draft.autoAdd).toBe(false);
		expect(draft.wanMonitorEnabled).toBe(false);
		expect(draft.tunnelProbe).toBe(false);
		expect(draft.udpEnabled).toBe(false);
		expect(draft.verbose).toBe(false);
		expect(draft.wanCheckUrl).toBe(AUTOLIST_DEFAULT_WAN_URL);
		expect(draft.resolveVia).toBe('');
		expect(draft.targetInterface).toBe('');
		expect(draft.excludeDomains).toEqual([]);
		expect(draft.excludeRuleSets).toEqual([]);
		expect(draft.addsPerHour).toBe(AUTOLIST_DEFAULT_ADDS_PER_HOUR);
	});

	it('keeps an explicit tunnelProbe=true and a saved WAN URL', () => {
		const draft = autolistConfigDraft({
			autoAdd: true,
			tunnelProbe: true,
			wanCheckUrl: 'https://example.com/health',
			targetInterface: 'awg0',
			resolveVia: 'sb:dns-quad9',
			addsPerHour: 100,
		});
		expect(draft.tunnelProbe).toBe(true);
		expect(draft.autoAdd).toBe(true);
		expect(draft.wanCheckUrl).toBe('https://example.com/health');
		expect(draft.targetInterface).toBe('awg0');
		expect(draft.resolveVia).toBe('sb:dns-quad9');
		expect(draft.addsPerHour).toBe(100);
	});
});

describe('mergeAutolistExclusions', () => {
	it('canonicalizes and merges selected domains into the current exclusions', () => {
		const merged = mergeAutolistExclusions(['Keep.EXAMPLE.', 'a.com'], ['WWW.B.com', 'b.com']);
		expect(merged).toEqual(['keep.example', 'a.com', 'www.b.com', 'b.com']);
	});

	it('returns null when any selected domain is an IP literal — the backend config rejects IPs', () => {
		expect(mergeAutolistExclusions(['a.com'], ['198.51.100.7'])).toBeNull();
		expect(mergeAutolistExclusions(['a.com'], ['2001:db8::1'])).toBeNull();
		expect(mergeAutolistExclusions(['a.com'], ['not a domain'])).toBeNull();
	});

	it('tolerates an absent current list and deduplicates case-insensitively', () => {
		expect(mergeAutolistExclusions(undefined, ['Example.COM'])).toEqual(['example.com']);
		expect(mergeAutolistExclusions(['example.com'], ['Example.com.'])).toEqual(['example.com']);
	});
});
