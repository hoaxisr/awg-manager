import { describe, it, expect, vi } from 'vitest';
import { render, fireEvent, screen } from '@testing-library/svelte';
import Awg3ImportModal from './Awg3ImportModal.svelte';

vi.mock('$lib/api/client', () => ({
	api: {
		awg3Import: vi.fn().mockResolvedValue(undefined),
	},
}));

describe('Awg3ImportModal', () => {
	it('closes the modal after a successful import', async () => {
		const onclose = vi.fn();
		const onimported = vi.fn();
		render(Awg3ImportModal, {
			props: { open: true, onclose, onimported },
		});

		await fireEvent.input(screen.getByPlaceholderText('имя туннеля'), {
			target: { value: 'tun0' },
		});
		await fireEvent.input(screen.getByPlaceholderText(/"type": "awg"/), {
			target: { value: '{"type":"awg","peers":[]}' },
		});

		await fireEvent.click(screen.getByRole('button', { name: 'Импортировать' }));

		expect(onimported).toHaveBeenCalledOnce();
		expect(onclose).toHaveBeenCalledOnce();
	});
});
