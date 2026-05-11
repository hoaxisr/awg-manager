import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import StagingBanner from './StagingBanner.svelte';

const { stagingWritable } = vi.hoisted(() => {
	const { writable } = require('svelte/store') as typeof import('svelte/store');
	const stagingWritable = writable<{ hasDraft: boolean; draftedAt?: string } | null>(null);
	return { stagingWritable };
});

vi.mock('$lib/stores/singboxRouter', () => ({
	singboxRouter: {
		staging: { subscribe: stagingWritable.subscribe },
	},
}));

vi.mock('$lib/api/client', () => ({
	api: {
		singboxRouterStagingApply: vi.fn(),
		singboxRouterStagingDiscard: vi.fn(),
	},
}));

// eslint-disable-next-line @typescript-eslint/no-explicit-any
let apiFn: any;

describe('StagingBanner', () => {
	beforeEach(async () => {
		vi.clearAllMocks();
		stagingWritable.set(null);
		apiFn = (await import('$lib/api/client')).api;
	});

	it('renders nothing when hasDraft is false', () => {
		stagingWritable.set({ hasDraft: false });
		const { container } = render(StagingBanner);
		expect(container.querySelector('.staging-banner')).toBeNull();
	});

	it('renders banner when hasDraft is true', () => {
		stagingWritable.set({ hasDraft: true, draftedAt: '2026-05-11T16:32:00Z' });
		const { container, getByText } = render(StagingBanner);
		expect(container.querySelector('.staging-banner')).toBeTruthy();
		expect(getByText('Применить')).toBeTruthy();
		expect(getByText('Сбросить')).toBeTruthy();
	});

	it('clicking Apply calls api.singboxRouterStagingApply', async () => {
		stagingWritable.set({ hasDraft: true, draftedAt: '2026-05-11T16:32:00Z' });
		vi.mocked(apiFn.singboxRouterStagingApply).mockResolvedValue(undefined);
		const { getByText } = render(StagingBanner);
		await fireEvent.click(getByText('Применить'));
		expect(apiFn.singboxRouterStagingApply).toHaveBeenCalledOnce();
	});

	it('on 422 with validation, renders error list inline', async () => {
		stagingWritable.set({ hasDraft: true, draftedAt: '2026-05-11T16:32:00Z' });
		vi.mocked(apiFn.singboxRouterStagingApply).mockRejectedValue({
			status: 422,
			body: {
				validation: {
					errors: [
						{
							slot: 'router',
							kind: 'unknown-outbound',
							tag: 'ghost',
							inRule: 'route.final',
							message: 'no slot declares this outbound tag',
						},
					],
				},
			},
		});
		const { getByText, findByText } = render(StagingBanner);
		await fireEvent.click(getByText('Применить'));
		await findByText(/no slot declares this outbound tag/);
	});

	it('clicking Discard shows confirm modal', async () => {
		stagingWritable.set({ hasDraft: true, draftedAt: '2026-05-11T16:32:00Z' });
		const { getByText, queryByText } = render(StagingBanner);
		expect(queryByText('Откатить правки?')).toBeNull();
		await fireEvent.click(getByText('Сбросить'));
		expect(queryByText('Откатить правки?')).toBeTruthy();
	});
});
