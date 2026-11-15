import { cleanup, fireEvent, render, screen } from '@testing-library/svelte';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import RecheckModal from './RecheckModal.svelte';
import { m } from '$lib/i18n';
import { api } from '$lib/api/client';
import { notifications } from '$lib/stores/notifications';
import type { AutolistCandidate } from '$lib/types';

function candidate(overrides: Partial<AutolistCandidate> = {}): AutolistCandidate {
	return {
		id: 'domain|example.com|tcp|443',
		target: { domain: 'example.com', host: 'www.example.com', ip: '93.184.216.34', network: 'tcp', port: 443 },
		reason: 'tcp-rst',
		state: 'waiting',
		firstAt: '2026-01-05T12:00:00Z',
		lastAt: '2026-01-05T13:00:00Z',
		coverage: { covered: false, durationMs: 12 },
		direct: { verdict: 'blocked', bytes: 0, durationMs: 5001, detail: 'timed out' },
		wan: { verdict: 'reachable', bytes: 892, durationMs: 420 },
		tunnel: { verdict: 'reachable', bytes: 1204, durationMs: 610 },
		targetInterface: 'nwg0',
		targetOutbound: 'nwg',
		correlationID: 'c0007',
		...overrides,
	};
}

beforeEach(() => {
	vi.spyOn(notifications, 'success').mockImplementation(() => '');
	vi.spyOn(notifications, 'error').mockImplementation(() => '');
});

afterEach(() => {
	cleanup();
	vi.restoreAllMocks();
});

describe('RecheckModal', () => {
	it('runs the recheck on open and shows the refreshed evidence grid', async () => {
		const refreshed = candidate({ direct: { verdict: 'blocked', durationMs: 12_345 } });
		const recheck = vi.spyOn(api, 'recheckAutolistCandidate').mockResolvedValue(refreshed);

		render(RecheckModal, {
			props: { open: true, candidate: candidate(), onclose: () => {} },
		});

		await vi.waitFor(() => expect(recheck).toHaveBeenCalledWith('domain|example.com|tcp|443'));
		await vi.waitFor(() => {
			expect(screen.getByText(m.autolist_reason_tcp_reset())).toBeTruthy();
			// Свежая длительность direct-проба из ответа сателлита.
			expect(screen.getByText(m.autolist_duration_s({ value: '12.3' }))).toBeTruthy();
		});
	});

	it('returns null as "candidate is gone" and stays open without a grid', async () => {
		vi.spyOn(api, 'recheckAutolistCandidate').mockResolvedValue(null);

		render(RecheckModal, {
			props: { open: true, candidate: candidate(), onclose: () => {} },
		});

		await vi.waitFor(() => expect(screen.getByText(m.autolist_recheck_gone())).toBeTruthy());
	});

	it('shows the error and allows to retry via "check again"', async () => {
		const recheck = vi
			.spyOn(api, 'recheckAutolistCandidate')
			.mockRejectedValueOnce(new Error('satellite is not answering'))
			.mockResolvedValue(candidate());

		render(RecheckModal, {
			props: { open: true, candidate: candidate(), onclose: () => {} },
		});

		await vi.waitFor(() => expect(screen.getByTestId('recheck-error')).toBeTruthy());
		await fireEvent.click(screen.getByRole('button', { name: m.autolist_recheck_again() }));
		await vi.waitFor(() => expect(recheck).toHaveBeenCalledTimes(2));
		await vi.waitFor(() => {
			expect(screen.getByText(m.autolist_reason_tcp_reset())).toBeTruthy();
			expect(screen.queryByTestId('recheck-error')).toBeNull();
		});
	});

	it('reports a per-ID satellite error string as a failure', async () => {
		vi.spyOn(api, 'recheckAutolistCandidate').mockRejectedValue(new Error('engine is stopped'));

		render(RecheckModal, {
			props: { open: true, candidate: candidate(), onclose: () => {} },
		});

		await vi.waitFor(() => {
			expect(screen.getByTestId('recheck-error')).toBeTruthy();
			expect(notifications.error).toHaveBeenCalledWith('engine is stopped');
		});
	});
});
