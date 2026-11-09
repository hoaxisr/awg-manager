import { cleanup, fireEvent, render, screen } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import AutolistTab from './AutolistTab.svelte';
import { api } from '$lib/api/client';
import type { AutolistStatus } from '$lib/types';

vi.stubGlobal('ResizeObserver', class {
	observe() {}
	disconnect() {}
});

const candidateStatus: AutolistStatus = {
	running: true,
	installed: true,
	enabled: true,
	candidates: [
		{
			domain: 'example.com',
			host: 'www.example.com',
			why: 'reset',
			state: 'candidate',
			fails: 1,
			sources: ['packet'],
			lastAt: new Date().toISOString(),
		},
	],
	history: [],
};

describe('AutolistTab manual addition', () => {
	afterEach(() => {
		cleanup();
		vi.restoreAllMocks();
		vi.unstubAllGlobals();
	});

	it('disables every action on the affected card until the request settles', async () => {
		vi.spyOn(api, 'getAutolistStatus').mockResolvedValue(candidateStatus);
		let settle!: (value: Record<string, string | null>) => void;
		const pending = new Promise<Record<string, string | null>>((resolve) => {
			settle = resolve;
		});
		const add = vi.spyOn(api, 'addAutolistCandidates').mockReturnValue(pending);

		render(AutolistTab);
		await screen.findByText('example.com');
		await fireEvent.click(screen.getByRole('checkbox', { name: 'example.com' }));
		await fireEvent.click(screen.getByRole('button', { name: 'В туннель' }));

		await screen.findByText('добавляется');
		expect(add).toHaveBeenCalledWith(['example.com']);
		expect(screen.getByRole('checkbox', { name: 'example.com' }).hasAttribute('disabled')).toBe(true);
		expect(screen.getByRole('button', { name: 'Проверить сейчас' }).hasAttribute('disabled')).toBe(true);
		for (const button of screen.getAllByRole('button', { name: 'Игнорировать навсегда' })) {
			expect(button.hasAttribute('disabled')).toBe(true);
		}
		expect(screen.getByRole('button', { name: 'Скрыть' }).hasAttribute('disabled')).toBe(true);
		expect(screen.getByRole('checkbox', { name: 'Выбрать все' }).hasAttribute('disabled')).toBe(true);
		expect(screen.getByRole('button', { name: 'В туннель' }).hasAttribute('disabled')).toBe(true);

		settle({ 'example.com': null });
		await screen.findByText('кандидат');
		expect(screen.getByRole('button', { name: 'Проверить сейчас' }).hasAttribute('disabled')).toBe(false);
		expect(screen.getByRole('button', { name: 'Скрыть' }).hasAttribute('disabled')).toBe(false);
	});
});
