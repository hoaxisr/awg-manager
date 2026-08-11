import { describe, expect, it } from 'vitest';
import { proxyClientOpsMode, proxyInOpsMode, proxyServerOpsMode } from './proxyOpsMode';

describe('proxyInOpsMode', () => {
	it('returns false for fresh instance', () => {
		expect(proxyInOpsMode({})).toBe(false);
	});

	it('returns true when running', () => {
		expect(proxyInOpsMode({ running: true })).toBe(true);
	});

	it('returns true when enabled or started before', () => {
		expect(proxyInOpsMode({ enabled: true })).toBe(true);
		expect(proxyInOpsMode({ startedAt: '2026-01-01T00:00:00Z' })).toBe(true);
	});

	it('returns true when setup is complete even after manual stop', () => {
		expect(proxyInOpsMode({ setupComplete: true })).toBe(true);
	});
});

describe('proxyClientOpsMode', () => {
	it('stays in ops panel when configured but stopped', () => {
		expect(
			proxyClientOpsMode({
				running: false,
				enabled: false,
				startedAt: '',
				setupComplete: true
			})
		).toBe(true);
	});
});

describe('proxyServerOpsMode', () => {
	it('stays in wizard for fresh instance without link', () => {
		expect(proxyServerOpsMode({})).toBe(false);
		expect(proxyServerOpsMode({ running: true })).toBe(false);
	});

	it('enters ops when link generated this session', () => {
		expect(proxyServerOpsMode({ running: true, generatedLink: 'wdtt://x' })).toBe(true);
	});

	it('enters ops after reboot when autostart is enabled but nothing started yet', () => {
		expect(proxyServerOpsMode({ enabled: true, startedAt: '', generatedLink: '' })).toBe(true);
	});

	it('enters ops on return visit via startedAt without link in memory', () => {
		expect(
			proxyServerOpsMode({
				running: true,
				startedAt: '2026-01-01T00:00:00Z',
				generatedLink: ''
			})
		).toBe(true);
	});

	it('enters ops when server config saved (setupComplete) after stop', () => {
		expect(
			proxyServerOpsMode({
				running: false,
				enabled: false,
				setupComplete: true
			})
		).toBe(true);
	});
});
