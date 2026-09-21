import { describe, expect, it } from 'vitest';
import { isTrustTunnelMultiAddress } from './trustTunnelImport';

describe('isTrustTunnelMultiAddress', () => {
	it('распознаёт 422 с кодом', () => {
		const err = Object.assign(new Error('x'), {
			status: 422,
			body: { code: 'TRUSTTUNNEL_MULTI_ADDRESS', data: { addresses: 3 } },
		});
		expect(isTrustTunnelMultiAddress(err)).toBe(3);
	});
	it('чужие ошибки — 0', () => {
		expect(isTrustTunnelMultiAddress(new Error('net'))).toBe(0);
		expect(
			isTrustTunnelMultiAddress(Object.assign(new Error('x'), { status: 422, body: { code: 'OTHER' } })),
		).toBe(0);
	});
});
