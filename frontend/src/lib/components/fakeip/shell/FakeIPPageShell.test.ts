import { describe, it, expect, vi, afterEach } from 'vitest';
import { render } from '@testing-library/svelte';
import { createRawSnippet } from 'svelte';
import FakeIPPageShell from './FakeIPPageShell.svelte';
import { fakeipConfig } from '$lib/stores/fakeipConfig';

vi.mock('$lib/api/client', () => ({ api: {} }));
vi.mock('$app/navigation', () => ({ goto: vi.fn() }));
vi.mock('$app/stores', async () => {
	const { readable } = await import('svelte/store');
	return { page: readable({ url: new URL('http://localhost/routing') }) };
});

vi.stubGlobal(
	'ResizeObserver',
	class {
		observe() {}
		unobserve() {}
		disconnect() {}
	},
);

afterEach(() => vi.restoreAllMocks());

describe('FakeIPPageShell', () => {
	it('сам загружает конфиг FakeIP: прямой заход на чип не видит пустой стор', () => {
		const loadAll = vi.spyOn(fakeipConfig, 'loadAll').mockResolvedValue();
		render(FakeIPPageShell, {
			props: {
				title: 'Outbounds',
				chips: [],
				activeChip: 'outbounds',
				onChipChange: vi.fn(),
				engineState: 'live',
				onRestart: vi.fn(),
				children: createRawSnippet(() => ({ render: () => '<div></div>' })),
			},
		});
		expect(loadAll).toHaveBeenCalledOnce();
	});
});
