import { describe, it, expect } from 'vitest';
import { nativewgUnavailableHint, supportsAwg3 } from './backendAvailability';

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

