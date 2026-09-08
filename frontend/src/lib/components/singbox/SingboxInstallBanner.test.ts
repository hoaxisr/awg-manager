import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render } from '@testing-library/svelte';
import SingboxInstallBanner from './SingboxInstallBanner.svelte';
import type { SingboxStatus } from '$lib/types';
import type { PollingState } from '$lib/stores/polling';

const { statusWritable } = vi.hoisted(() => {
	const { writable } = require('svelte/store') as typeof import('svelte/store');
	const statusWritable = writable<PollingState<SingboxStatus>>({
		data: null,
		status: 'idle',
		error: null,
		lastFetchedAt: 0,
		consecutiveFailures: 0,
	});
	return { statusWritable };
});

vi.mock('$lib/stores/singbox', () => ({
	singboxStatus: {
		subscribe: statusWritable.subscribe,
		applyMutationResponse: vi.fn(),
		invalidate: vi.fn(),
		refetch: vi.fn(),
	},
}));

vi.mock('$lib/stores/singboxInstall', () => ({
	singboxInstallProgress: { subscribe: vi.fn((run) => { run(null); return () => {}; }) },
}));

vi.mock('$lib/api/client', () => ({
	api: {
		singboxInstall: vi.fn(),
		singboxUpdate: vi.fn(),
	},
}));

function setStatus(data: SingboxStatus): void {
	statusWritable.set({
		data,
		status: 'fresh',
		error: null,
		lastFetchedAt: Date.now(),
		consecutiveFailures: 0,
	});
}

const baseStatus: SingboxStatus = {
	installed: true,
	running: false,
	tunnelCount: 0,
	proxyComponent: true,
	ndmsProxyEnabled: true,
	updateAvailable: false,
	requiredVersion: '1.10.0',
};

describe('SingboxInstallBanner', () => {
	beforeEach(() => {
		vi.clearAllMocks();
		// Reset localStorage so dismiss state doesn't bleed between tests
		vi.stubGlobal('localStorage', {
			getItem: vi.fn(() => null),
			setItem: vi.fn(),
		});
		statusWritable.set({
			data: null,
			status: 'idle',
			error: null,
			lastFetchedAt: 0,
			consecutiveFailures: 0,
		});
	});

	it('renders missing-no-space state', () => {
		setStatus({
			...baseStatus,
			installed: false,
			installState: 'missing_no_space',
			requiredBytes: 50_000_000,
			freeBytes: 10_000_000,
		});
		const { getByText } = render(SingboxInstallBanner);
		expect(getByText(/sing-box не установлен/i)).toBeTruthy();
		expect(getByText(/Не хватает места/i)).toBeTruthy();
	});

	it('renders outdated-no-space state', () => {
		setStatus({
			...baseStatus,
			installed: true,
			installState: 'outdated_no_space',
			updateAvailable: true,
			currentVersion: '1.9.0',
			requiredVersion: '1.10.0',
			requiredBytes: 30_000_000,
			freeBytes: 5_000_000,
		});
		const { getByText } = render(SingboxInstallBanner);
		expect(getByText(/Обновление sing-box недоступно/i)).toBeTruthy();
	});

	it('does not render missing-no-space when installState is missing (not missing_no_space)', () => {
		setStatus({
			...baseStatus,
			installed: false,
			installState: 'missing',
		});
		const { queryByText } = render(SingboxInstallBanner);
		expect(queryByText(/Не хватает места/i)).toBeNull();
	});

	it('renders nothing for a build without with_naive_outbound (tags are not a banner concern)', () => {
		setStatus({
			...baseStatus,
			installState: 'installed',
			currentVersion: '1.10.0',
			features: ['with_gvisor', 'with_quic'],
		});
		const { container } = render(SingboxInstallBanner);
		expect(container.querySelector('.banner')).toBeNull();
	});

	it('does not render outdated-no-space when updateAvailable without installState', () => {
		setStatus({
			...baseStatus,
			installed: true,
			updateAvailable: true,
			currentVersion: '1.9.0',
			installState: undefined,
		});
		const { queryByText } = render(SingboxInstallBanner);
		expect(queryByText(/Обновление sing-box недоступно/i)).toBeNull();
	});
});
