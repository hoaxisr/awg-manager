import { describe, it, expect, vi, beforeEach } from 'vitest';
import { ServersClient } from './clientServers';
import type { CapabilitiesResponse } from '$lib/types/serverWizard';

type RequestableClient = {
	request: (endpoint: string, init?: RequestInit) => Promise<unknown>;
};

describe('ServersClient getWizardCapabilities normalization (Phase 0)', () => {
	let client: ServersClient;
	const asRequestable = (c: ServersClient): RequestableClient => c as unknown as RequestableClient;

	beforeEach(() => {
		client = new ServersClient();
	});

	it('safely populates empty arrays when backend returns minimal or empty data', async () => {
		vi.spyOn(asRequestable(client), 'request').mockResolvedValue({});

		const res = await client.getWizardCapabilities('xray');

		expect(res).toBeDefined();
		expect(res.kind).toBe('xray');
		expect(Array.isArray(res.profiles)).toBe(true);
		expect(res.profiles).toEqual([]);
		expect(Array.isArray(res.egress_options)).toBe(true);
		expect(res.egress_options).toEqual([]);
		expect(Array.isArray(res.modes)).toBe(true);
		expect(res.modes).toEqual([]);
		expect(Array.isArray(res.scenarios)).toBe(true);
		expect(res.scenarios).toEqual([]);
		expect(res.configured).toBe(false);
		expect(res.running).toBe(false);
		expect(res.recovery_required).toBe(false);
	});

	it('preserves complete capabilities when returned by server', async () => {
		const mockResponse: CapabilitiesResponse = {
			kind: 'tgwebproxy',
			scenarios: ['dual', 'direct_fake_tls'],
			modes: ['get', 'websocket'],
			profiles: [
				{
					id: 'cdn_get',
					name: 'CDN GET',
					description: 'Test',
					capabilities: ['get_only'],
					instructions: 'Instructions',
					recommended: true
				}
			],
			egress_options: [
				{
					id: 'direct',
					name: 'Direct WAN',
					kind: 'direct',
					owner: 'system',
					available: true,
					supports_tcp: true,
					supports_udp: true,
					generation: 1
				}
			],
			configured: true,
			running: true,
			recovery_required: false
		};

		vi.spyOn(asRequestable(client), 'request').mockResolvedValue(mockResponse);

		const res = await client.getWizardCapabilities('tgwebproxy');

		expect(res.kind).toBe('tgwebproxy');
		expect(res.scenarios).toEqual(['dual', 'direct_fake_tls']);
		expect(res.modes).toEqual(['get', 'websocket']);
		expect(res.profiles.length).toBe(1);
		expect(res.profiles[0].id).toBe('cdn_get');
		expect(res.egress_options.length).toBe(1);
		expect(res.egress_options[0].id).toBe('direct');
		expect(res.configured).toBe(true);
		expect(res.running).toBe(true);
		expect(res.recovery_required).toBe(false);
	});

	it('normalizes undefined or null fields without throwing exceptions', async () => {
		const partialResponse = {
			kind: 'xray',
			profiles: null,
			egress_options: undefined,
			modes: null,
			scenarios: undefined,
			configured: 1,
			running: 'true',
			recovery_required: 0
		};

		vi.spyOn(asRequestable(client), 'request').mockResolvedValue(partialResponse);

		const res = await client.getWizardCapabilities('xray');

		expect(res.kind).toBe('xray');
		expect(res.profiles).toEqual([]);
		expect(res.egress_options).toEqual([]);
		expect(res.modes).toEqual([]);
		expect(res.scenarios).toEqual([]);
		expect(res.configured).toBe(true);
		expect(res.running).toBe(true);
		expect(res.recovery_required).toBe(false);
	});

	it('propagates network rejections and API errors cleanly for UI error handling', async () => {
		vi.spyOn(asRequestable(client), 'request').mockRejectedValue(
			new Error('Network connection failed (502 Bad Gateway)')
		);

		await expect(client.getWizardCapabilities('xray')).rejects.toThrow(
			'Network connection failed (502 Bad Gateway)'
		);
	});

	it('handles completely null or primitive response values without crashing', async () => {
		vi.spyOn(asRequestable(client), 'request').mockResolvedValue(null);

		const res = await client.getWizardCapabilities('tgwebproxy');

		expect(res).toBeDefined();
		expect(res.kind).toBe('tgwebproxy');
		expect(res.profiles).toEqual([]);
		expect(res.egress_options).toEqual([]);
		expect(res.modes).toEqual([]);
		expect(res.scenarios).toEqual([]);
		expect(res.configured).toBe(false);
	});

	it('successfully recovers after a failed attempt on subsequent retry', async () => {
		const requestSpy = vi
			.spyOn(asRequestable(client), 'request')
			.mockRejectedValueOnce(new Error('Temporary router timeout'))
			.mockResolvedValueOnce({
				kind: 'xray',
				profiles: [{ id: 'cdn_get', name: 'CDN GET' }],
				egress_options: [{ id: 'direct', name: 'Direct' }]
			});

		// 1st attempt: fails
		await expect(client.getWizardCapabilities('xray')).rejects.toThrow('Temporary router timeout');

		// 2nd attempt (retry): succeeds
		const recovered = await client.getWizardCapabilities('xray');
		expect(recovered).toBeDefined();
		expect(recovered.profiles.length).toBe(1);
		expect(recovered.profiles[0].id).toBe('cdn_get');
		expect(recovered.egress_options.length).toBe(1);
		expect(recovered.egress_options[0].id).toBe('direct');
	});

	it('does not infer cert_ok=true from generic ssl_enabled or acme_v2 flags', async () => {
		vi.spyOn(asRequestable(client), 'request').mockResolvedValue({
			kind: 'xray',
			keendns: {
				state: 'supported',
				base_fqdn: 'example.keenetic.pro',
				access_mode: 'cloud',
				ssl_enabled: true,
				acme_v2: true,
				publications: [
					{
						label: 'connect',
						fqdn: 'connect.example.keenetic.pro',
						upstream_host: '127.0.0.1',
						upstream_port: 9009,
						compatible_with_ingress: true,
						runtime_active: true
					}
				]
			}
		});

		const res = await client.getWizardCapabilities('xray');
		expect(res.keendns).toBeDefined();
		expect(res.keendns?.cert_ok).toBe(false);
	});
});
