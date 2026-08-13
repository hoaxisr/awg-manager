import { describe, it, expect } from 'vitest';
import { nativewgUnavailableHint, supportsAwg3, supportsAwg31 } from './backendAvailability';

describe('nativewgUnavailableHint', () => {
	it('explains the missing WireGuard component', () => {
		expect(nativewgUnavailableHint('no-component')).toContain('компонент WireGuard');
	});

	it('explains the missing obfuscation path', () => {
		expect(nativewgUnavailableHint('no-obfuscation')).toContain('awg_proxy');
	});

	it('returns empty for available / unknown reasons', () => {
		expect(nativewgUnavailableHint('')).toBe('');
		expect(nativewgUnavailableHint(undefined)).toBe('');
		expect(nativewgUnavailableHint('something-else')).toBe('');
	});
});

describe('supportsAwg3', () => {
	it('accepts the 3.x kernel module', () => {
		expect(supportsAwg3('3.0.20260731')).toBe(true);
	});

	it('rejects older modules and a missing one', () => {
		expect(supportsAwg3('1.0.20251009')).toBe(false);
		expect(supportsAwg3('')).toBe(false);
		expect(supportsAwg3(undefined)).toBe(false);
	});
});

describe('supportsAwg31', () => {
	it('rejects 3.0, which supportsAwg3 accepts', () => {
		expect(supportsAwg3('3.0.20260805')).toBe(true);
		expect(supportsAwg31('3.0.20260805')).toBe(false);
	});

	it('accepts 3.1 and above', () => {
		expect(supportsAwg31('3.1.20260812')).toBe(true);
		expect(supportsAwg31('3.2.20270101')).toBe(true);
		expect(supportsAwg31('4.0.20270101')).toBe(true);
	});

	it('rejects older modules, a missing one and garbage', () => {
		expect(supportsAwg31('1.0.20251009')).toBe(false);
		expect(supportsAwg31('')).toBe(false);
		expect(supportsAwg31(undefined)).toBe(false);
		expect(supportsAwg31('unknown')).toBe(false);
	});
});
