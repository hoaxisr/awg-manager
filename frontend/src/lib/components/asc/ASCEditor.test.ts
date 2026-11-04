import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';

// ASCEditor imports the settings barrel, which pulls in the theme store
// (reads matchMedia on import). Stub it before imports resolve — vi.hoisted.
vi.hoisted(() => {
	Object.defineProperty(globalThis, 'matchMedia', {
		writable: true,
		configurable: true,
		value: (query: string) => ({
			matches: false,
			media: query,
			onchange: null,
			addEventListener: () => {},
			removeEventListener: () => {},
			addListener: () => {},
			removeListener: () => {},
			dispatchEvent: () => false,
		}),
	});
});

import ASCEditor from './ASCEditor.svelte';
import type { ASCParamsExtended } from '$lib/types';

function params(): ASCParamsExtended {
	return {
		jc: 4, jmin: 40, jmax: 70, s1: 0, s2: 0,
		h1: '1', h2: '2', h3: '3', h4: '4',
		s3: 0, s4: 0, i1: '', i2: '', i3: '', i4: '', i5: '',
	};
}

describe('ASCEditor awg3 section', () => {
	it('renders the AWG 3.0 params when awg3 is true', () => {
		render(ASCEditor, { props: { params: params(), extended: true, awg3: true } });
		expect(screen.getByText('AmneziaWG 3.0')).toBeTruthy();
		expect(screen.getByPlaceholderText(/base64-ключ/)).toBeTruthy();
	});

	it('hides the AWG 3.0 params when awg3 is false', () => {
		render(ASCEditor, { props: { params: params(), extended: true, awg3: false } });
		expect(screen.queryByText('AmneziaWG 3.0')).toBeNull();
		expect(screen.queryByPlaceholderText(/base64-ключ/)).toBeNull();
	});
});

describe('ASCEditor awg_proxy path', () => {
	const limited = (extra: Partial<ASCParamsExtended>) =>
		render(ASCEditor, {
			props: { params: { ...params(), ...extra }, extended: true, awg3: true, awg3Limited: true },
		});

	it('names imported params that are not applied', () => {
		limited({ rekeyTimeout: '5', disableCookies: true });
		expect(screen.getByText(/RekeyTimeout, DisableCookies/)).toBeTruthy();
	});

	it('does not show DisableCookies as enabled', () => {
		limited({ disableCookies: true });
		expect(screen.queryByText('AmneziaWG 3.1')).toBeNull();
	});

	it('keeps RandomTrailers: it does work on this path', () => {
		limited({ randomTrailers: true });
		expect(screen.getByText('AmneziaWG 3.1')).toBeTruthy();
		expect(screen.queryByText(/не применяются|not applied/)).toBeNull();
	});

	it('stays silent on the full path', () => {
		render(ASCEditor, {
			props: { params: { ...params(), rekeyTimeout: '5' }, extended: true, awg3: true },
		});
		expect(screen.queryByText(/не применяются|not applied/)).toBeNull();
	});
});
