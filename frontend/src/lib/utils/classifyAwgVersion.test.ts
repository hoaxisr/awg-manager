import { describe, it, expect } from 'vitest';
import { classifyAwgVersionFromAsc } from './classifyAwgVersion';
import type { ASCParams } from '$lib/types';
import type { AwgValue } from '$lib/components/ui/VersionBadge.svelte';

/** Mirrors internal/tunnel/config/config_test.go — ClassifyAWGVersion table. */
describe('classifyAwgVersionFromAsc', () => {
	it.each<{
		name: string;
		params: ASCParams | null | undefined;
		want: AwgValue;
	}>([
		{ name: 'null params', params: null, want: 'wg' },
		{ name: 'undefined params', params: undefined, want: 'wg' },
		{ name: 'empty params', params: {} as ASCParams, want: 'wg' },
		{
			name: 'AWG 1.0 — all H values',
			params: { h1: '111', h2: '222', h3: '333', h4: '444' } as ASCParams,
			want: 'awg1.0',
		},
		{
			name: 'AWG 1.0 partial H — WG',
			params: { h1: '111', h2: '222' } as ASCParams,
			want: 'wg',
		},
		{
			name: 'AWG 1.5 — I1 with full H',
			params: { h1: '111', h2: '222', h3: '333', h4: '444', i1: 'AABB' } as ASCParams,
			want: 'awg1.5',
		},
		{
			name: 'AWG 1.5 — any signature packet',
			params: { i3: 'AABB' } as ASCParams,
			want: 'awg1.5',
		},
		{
			name: 'AWG 1.5 takes priority over AWG 1.0',
			params: { h1: '111', h2: '222', h3: '333', h4: '444', i1: 'sig' } as ASCParams,
			want: 'awg1.5',
		},
		{
			name: 'AWG 2.0 — H1 range',
			params: { h1: '100-200', h2: '222', h3: '333', h4: '444', i1: 'AABB' } as ASCParams,
			want: 'awg2.0',
		},
		{
			name: 'AWG 2.0 — any H range',
			params: { h1: '111', h2: '222', h3: '10-20', h4: '444' } as ASCParams,
			want: 'awg2.0',
		},
		{
			name: 'AWG 3.0 — header protection',
			params: { h1: '1', h2: '2', h3: '3', h4: '4', headerProtectionKey: 'a2V5' } as ASCParams,
			want: 'awg3',
		},
		{
			name: 'AWG 3.0 — any timer param',
			params: { rekeyAfterTime: '120-150' } as ASCParams,
			want: 'awg3',
		},
		{
			name: 'AWG 3.0 takes priority over AWG 2.0',
			params: { h1: '100-200', h2: '2', h3: '3', h4: '4', i1: 'sig', maxHandshakeAttempts: '5' } as ASCParams,
			want: 'awg3',
		},
		{
			name: 'AWG 3.1 — RandomTrailers',
			params: { randomTrailers: true } as ASCParams,
			want: 'awg3.1',
		},
		{
			name: 'AWG 3.1 — DisableCookies',
			params: { disableCookies: true } as ASCParams,
			want: 'awg3.1',
		},
		{
			name: 'AWG 3.1 takes priority over AWG 3.0',
			params: { headerProtectionKey: 'a2V5', randomTrailers: true } as ASCParams,
			want: 'awg3.1',
		},
		{
			name: 'both flags off stay out of the way',
			params: { h1: '1', h2: '2', h3: '3', h4: '4', randomTrailers: false, disableCookies: false } as ASCParams,
			want: 'awg1.0',
		},
	])('$name → $want', ({ params, want }) => {
		expect(classifyAwgVersionFromAsc(params)).toBe(want);
	});
});
