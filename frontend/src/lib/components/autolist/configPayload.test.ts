import { describe, expect, it } from 'vitest';
import { buildAutolistConfigPayload } from './configPayload';

describe('buildAutolistConfigPayload', () => {
	it('keeps cleared strings, explicit false values, and empty arrays in the JSON body', () => {
		const payload = {
			path: '/config',
			body: buildAutolistConfigPayload({
				autoAdd: false,
				targetInterface: '',
				targetOutbound: '',
				resolveVia: 'awgm',
				resolveIPEnabled: false,
				probeInterface: '',
				verbose: false,
				packetEnabled: false,
				instant: false,
				tunnelProbe: false,
				wanCheckUrl: '',
				excludeDomains: [],
				excludeRuleSets: [],
			}),
		};

		expect(JSON.parse(JSON.stringify(payload))).toEqual({
			path: '/config',
			body: {
				autoAdd: false,
				targetInterface: '',
				targetOutbound: '',
				resolveVia: 'awgm',
				resolveIPEnabled: false,
				probeInterface: '',
				verbose: false,
				packetEnabled: false,
				instant: false,
				tunnelProbe: false,
				wanCheckUrl: '',
				excludeDomains: [],
				excludeRuleSets: [],
			},
		});
	});
});
