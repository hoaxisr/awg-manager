import { describe, expect, it } from 'vitest';
import { ruleSetDownloadDetour, ruleSetNamedHTTPClient } from './ruleSetDetour';

describe('ruleSetNamedHTTPClient', () => {
	it('returns the client name for string-form http_client', () => {
		expect(ruleSetNamedHTTPClient({ http_client: 'my-client' })).toBe('my-client');
	});

	it('returns null for object-form http_client', () => {
		expect(ruleSetNamedHTTPClient({ http_client: { detour: 'vpn1' } })).toBeNull();
	});

	it('returns null when http_client is absent', () => {
		expect(ruleSetNamedHTTPClient({})).toBeNull();
	});
});

describe('ruleSetDownloadDetour', () => {
	it('reads the sing-box ≥1.14 http_client.detour form', () => {
		expect(ruleSetDownloadDetour({ http_client: { detour: 'vpn1' } })).toBe('vpn1');
	});

	it('falls back to legacy download_detour', () => {
		expect(ruleSetDownloadDetour({ download_detour: 'vpn1' })).toBe('vpn1');
	});

	it('prefers http_client over legacy field', () => {
		expect(ruleSetDownloadDetour({ http_client: { detour: 'new' }, download_detour: 'old' })).toBe('new');
	});

	it('treats a named client (string form) as automatic', () => {
		expect(ruleSetDownloadDetour({ http_client: 'my-client' })).toBe('');
	});

	it('returns empty string when nothing is set', () => {
		expect(ruleSetDownloadDetour({})).toBe('');
	});
});
