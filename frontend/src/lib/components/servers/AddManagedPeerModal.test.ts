import { describe, it, expect, vi } from 'vitest';
import { render, fireEvent, waitFor } from '@testing-library/svelte';
import AddManagedPeerModal from './AddManagedPeerModal.svelte';
import { api } from '$lib/api/client';
import type { ManagedServer } from '$lib/types';

vi.mock('$lib/api/client', () => ({ api: { addManagedPeer: vi.fn(), getManagedPeerPresets: vi.fn() } }));
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

describe('AddManagedPeerModal: сети клиента', () => {
	it('пресет «Всё, кроме сетей роутера» подставляет exceptRouter, тело содержит сети', async () => {
		vi.mocked(api.getManagedPeerPresets).mockResolvedValue({
			routerOnly: '10.8.0.0/24',
			exceptRouter: '0.0.0.0/5, 8.0.0.0/7, ::/0'
		});
		vi.mocked(api.addManagedPeer).mockResolvedValue(
			{} as Awaited<ReturnType<typeof api.addManagedPeer>>
		);
		const { getByText, getByLabelText } = render(AddManagedPeerModal, {
			open: true,
			serverId: 'srv',
			server: baseServer(),
			onclose: vi.fn(),
			onAdded: vi.fn(),
		});

		await fireEvent.click(getByText('Всё, кроме сетей роутера'));
		await waitFor(() =>
			expect((getByLabelText('AllowedIPs клиента') as HTMLTextAreaElement).value).toBe(
				'0.0.0.0/5,\n8.0.0.0/7,\n::/0'
			)
		);
		expect(api.getManagedPeerPresets).toHaveBeenCalledWith('srv', '');

		await fireEvent.click(getByText('Добавить'));
		expect(api.addManagedPeer).toHaveBeenCalledWith(
			'srv',
			expect.objectContaining({ clientAllowedIPs: '0.0.0.0/5, 8.0.0.0/7, ::/0', remoteSubnets: [] })
		);
	});
});

describe('AddManagedPeerModal: сервер с LAN-сегментами', () => {
	it('поле «Сети за клиентом» доступно: ACL сервера пропускает эти сети', () => {
		const { getByLabelText, queryByText } = render(AddManagedPeerModal, {
			open: true,
			serverId: 'srv',
			server: baseServer({ lanSegments: ['Home'] }),
			onclose: vi.fn(),
			onAdded: vi.fn(),
		});
		expect((getByLabelText('Сети за клиентом') as HTMLTextAreaElement).disabled).toBe(false);
		expect(queryByText(/LAN-сегментам/)).toBeNull();
	});
});
