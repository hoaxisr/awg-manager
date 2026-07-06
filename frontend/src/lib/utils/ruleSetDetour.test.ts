import { describe, expect, it } from 'vitest';
import { ruleSetDownloadDetour } from './ruleSetDetour';

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
