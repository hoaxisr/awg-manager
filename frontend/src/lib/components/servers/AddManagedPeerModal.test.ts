import { describe, it, expect, vi } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import AddManagedPeerModal from './AddManagedPeerModal.svelte';
import type { ManagedServer } from '$lib/types';

vi.mock('$lib/api/client', () => ({ api: { addManagedPeer: vi.fn() } }));
vi.mock('$lib/stores/notifications', () => ({ notifications: { success: vi.fn(), error: vi.fn() } }));

function baseServer(over: Partial<ManagedServer> = {}): ManagedServer {
	return {
		interfaceName: 'awgm0',
		address: '10.8.0.1',
		mask: '255.255.255.0',
		listenPort: 51820,
		policy: '',
		peers: [],
		...over,
	};
}

describe('AddManagedPeerModal', () => {
	it('disables Add and shows an error for a Tunnel IP without a prefix, enables on fix', async () => {
		const { getByText, getByLabelText, baseElement } = render(AddManagedPeerModal, {
			open: true,
			serverId: 'srv',
			server: baseServer(),
			onclose: vi.fn(),
			onAdded: vi.fn(),
		});

		const addButton = () => getByText('Добавить').closest('button') as HTMLButtonElement;
		const ipInput = getByLabelText('Tunnel IP (CIDR)');

		await fireEvent.input(ipInput, { target: { value: '10.0.0.2' } });
		expect(addButton().disabled).toBe(true);
		expect(baseElement.querySelector('.field-hint.is-error')?.textContent).toMatch(/префикс/);

		await fireEvent.input(ipInput, { target: { value: '10.0.0.2/32' } });
		expect(addButton().disabled).toBe(false);
		expect(baseElement.querySelector('.field-hint.is-error')).toBeFalsy();
	});
});
