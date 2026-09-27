import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import ForeignIfacePanel from './ForeignIfacePanel.svelte';

vi.mock('$lib/api/client', () => ({
	api: {
		listForeignIfaceCandidates: vi.fn(async () => [{ name: 'opkgtun7', label: 'csqtt', kind: 'opkgtun', up: true }]),
		markForeignIface: vi.fn(async () => ({ ok: true })),
	},
}));

describe('ForeignIfacePanel', () => {
	it('marks candidate and reports picked name', async () => {
		const onpicked = vi.fn();
		render(ForeignIfacePanel, { onpicked });
		await fireEvent.click(screen.getByText(/Интерфейс другой программы/));
		await fireEvent.click(await screen.findByRole('button', { name: /Отметить и выбрать «csqtt»/ }));
		await waitFor(() => expect(onpicked).toHaveBeenCalledWith('opkgtun7'));
	});

	it('shows server rejection and does not pick', async () => {
		const { api } = await import('$lib/api/client');
		vi.mocked(api.markForeignIface).mockRejectedValueOnce(new Error('ppp0 — интерфейс роутера'));
		const onpicked = vi.fn();
		render(ForeignIfacePanel, { onpicked });
		await fireEvent.click(screen.getByText(/Интерфейс другой программы/));
		await fireEvent.input(screen.getByPlaceholderText('имя интерфейса ядра'), { target: { value: 'ppp0' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Отметить и выбрать' }));
		expect(await screen.findByText(/интерфейс роутера/)).toBeTruthy();
		expect(onpicked).not.toHaveBeenCalled();
	});
});
