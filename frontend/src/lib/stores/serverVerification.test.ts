import { describe, it, expect, beforeEach } from 'vitest';
import { get } from 'svelte/store';
import {
	lastServerVerification,
	recordServerVerification,
	pickFresherVerificationReport,
	clearServerVerification,
	resetServerVerification,
} from './serverVerification';
import type { VerificationReport } from '$lib/types/serverWizard';

function makeReport(overrides: Partial<VerificationReport> = {}): VerificationReport {
	return {
		kind: 'tgwebproxy',
		checked_at: '2026-11-21T15:31:38Z',
		state_fingerprint: 'fp-1',
		scope: 'router',
		scope_note: 'Local router check',
		overall_status: 'ok',
		checks: [
			{
				id: 'chk-1',
				service: 'tg_mtproto',
				title: 'MTProto',
				status: 'ok',
				message: 'OK',
			},
		],
		...overrides,
	};
}

describe('serverVerification store helpers', () => {
	beforeEach(() => {
		resetServerVerification();
	});

	it('returns current reference when candidate is equivalent with identical timestamp', () => {
		const current = makeReport();
		const candidate = makeReport();

		const picked = pickFresherVerificationReport(candidate, current);
		expect(picked).toBe(current);
	});

	it('returns candidate when strictly newer or when fingerprint/status differs at same timestamp', () => {
		const older = makeReport({ checked_at: '2026-11-21T15:31:38Z', state_fingerprint: 'fp-1' });
		const newer = makeReport({ checked_at: '2026-11-21T15:32:00Z', state_fingerprint: 'fp-1' });
		const changedSameTs = makeReport({ checked_at: '2026-11-21T15:31:38Z', state_fingerprint: 'fp-2' });

		expect(pickFresherVerificationReport(newer, older)).toBe(newer);
		expect(pickFresherVerificationReport(older, newer)).toBe(newer);
		expect(pickFresherVerificationReport(changedSameTs, older)).toBe(changedSameTs);
	});

	it('clearServerVerification clears only the targeted server kind', () => {
		const xrayRep = makeReport({ kind: 'xray' });
		const tgRep = makeReport({ kind: 'tgwebproxy' });

		recordServerVerification('xray', xrayRep);
		recordServerVerification('tgwebproxy', tgRep);

		clearServerVerification('xray');
		expect(get(lastServerVerification)).toEqual({ xray: null, tgwebproxy: tgRep });

		clearServerVerification('tgwebproxy');
		expect(get(lastServerVerification)).toEqual({ xray: null, tgwebproxy: null });

		recordServerVerification('both', xrayRep);
		expect(get(lastServerVerification)).toEqual({ xray: xrayRep, tgwebproxy: xrayRep });
		clearServerVerification('both');
		expect(get(lastServerVerification)).toEqual({ xray: null, tgwebproxy: null });
	});
});
