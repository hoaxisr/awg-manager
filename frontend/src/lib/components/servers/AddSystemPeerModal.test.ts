import { describe, it, expect, vi } from 'vitest';
import { render, fireEvent, waitFor } from '@testing-library/svelte';
import AddSystemPeerModal from './AddSystemPeerModal.svelte';
import { api } from '$lib/api/client';
import type { WireguardServer } from '$lib/types';

vi.mock('$lib/api/client', () => ({
	api: { addSystemServerPeer: vi.fn(), getSystemServerPeerPresets: vi.fn() }
}));
vi.mock('$lib/stores/notifications', () => ({ notifications: { success: vi.fn(), error: vi.fn() } }));
vi.mock('$lib/stores/servers', () => ({ servers: { applyMutationResponse: vi.fn() } }));

const server = { id: 'Wireguard0', address: '10.9.0.1', peers: [] } as unknown as WireguardServer;

function openModal() {
	return render(AddSystemPeerModal, {
		open: true,
		serverId: 'Wireguard0',
		server,
		onclose: vi.fn(),
		onAdded: vi.fn()
	});
}

describe('AddSystemPeerModal: сети клиента', () => {
	it('пресет подставляет строку в поле, dns уходит в запрос пресета', async () => {
		vi.mocked(api.getSystemServerPeerPresets).mockResolvedValue({
			routerOnly: '10.9.0.0/24, 192.168.1.0/24',
			exceptRouter: '0.0.0.0/1, ::/0'
		});
		const { getByText, getByLabelText } = openModal();
		await fireEvent.input(getByLabelText('DNS серверы'), { target: { value: '9.9.9.9' } });
		await fireEvent.click(getByText('Только сети роутера'));
		await waitFor(() =>
			expect((getByLabelText('AllowedIPs клиента') as HTMLInputElement).value).toBe(
				'10.9.0.0/24, 192.168.1.0/24'
			)
		);
		expect(api.getSystemServerPeerPresets).toHaveBeenCalledWith('Wireguard0', '9.9.9.9');
	});

	it('шлёт сети за клиентом массивом и блокирует кнопку на невалидном вводе', async () => {
		vi.mocked(api.addSystemServerPeer).mockResolvedValue(
			{} as Awaited<ReturnType<typeof api.addSystemServerPeer>>
		);
		const { getByText, getByLabelText } = openModal();
		const add = () => getByText('Добавить').closest('button') as HTMLButtonElement;
		await fireEvent.input(getByLabelText('Сети за клиентом'), {
			target: { value: '10.0.0.0/8\n10.1.0.0/16' }
		});
		expect(add().disabled).toBe(true);
		await fireEvent.input(getByLabelText('Сети за клиентом'), {
			target: { value: '192.168.77.0/24\n192.168.78.0/24' }
		});
		expect(add().disabled).toBe(false);
		await fireEvent.click(add());
		expect(api.addSystemServerPeer).toHaveBeenCalledWith(
			'Wireguard0',
			expect.objectContaining({
				clientAllowedIPs: '',
				remoteSubnets: ['192.168.77.0/24', '192.168.78.0/24']
			})
		);
	});
});
