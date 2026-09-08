import { describe, it, expect, vi } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import EditSystemPeerModal from './EditSystemPeerModal.svelte';
import { api } from '$lib/api/client';
import type { WireguardServerPeer } from '$lib/types';

vi.mock('$lib/api/client', () => ({
	api: { updateSystemServerPeer: vi.fn(), generateSignature: vi.fn() }
}));
vi.mock('$lib/stores/notifications', () => ({ notifications: { success: vi.fn(), error: vi.fn() } }));
vi.mock('$lib/stores/servers', () => ({ servers: { applyMutationResponse: vi.fn() } }));

function basePeer(over: Partial<WireguardServerPeer> = {}): WireguardServerPeer {
	return {
		publicKey: 'pk',
		description: 'client',
		endpoint: '',
		allowedIPs: ['10.9.0.2/32'],
		rxBytes: 0,
		txBytes: 0,
		lastHandshake: '',
		online: false,
		enabled: true,
		confAvailable: true,
		...over
	};
}

function openModal(peer: WireguardServerPeer) {
	return render(EditSystemPeerModal, {
		open: true,
		serverId: 'Wireguard0',
		peer,
		onclose: vi.fn(),
		onUpdated: vi.fn()
	});
}

describe('EditSystemPeerModal', () => {
	it('отправляет сгенерированную сигнатуру вместе с описанием', async () => {
		vi.mocked(api.generateSignature).mockResolvedValue({
			ok: true,
			source: 'builtin',
			protocol: 'quic_initial',
			byteSize: 1200,
			packets: { i1: '<b 0xc0ffee>', i2: '', i3: '', i4: '', i5: '' }
		});
		vi.mocked(api.updateSystemServerPeer).mockResolvedValue(
			{} as Awaited<ReturnType<typeof api.updateSystemServerPeer>>
		);
		const { getByText } = openModal(basePeer());

		await fireEvent.click(getByText('Сгенерировать'));
		await fireEvent.click(getByText('Сохранить'));

		expect(api.updateSystemServerPeer).toHaveBeenCalledWith('Wireguard0', 'pk', {
			description: 'client',
			tunnelIP: '10.9.0.2/32',
			signature: { profile: 'quic_initial', i1: '<b 0xc0ffee>', i2: '', i3: '', i4: '', i5: '' }
		});
	});

	it('без правки сигнатуры поля signature в теле нет', async () => {
		vi.mocked(api.updateSystemServerPeer).mockResolvedValue(
			{} as Awaited<ReturnType<typeof api.updateSystemServerPeer>>
		);
		const { getByText, getByLabelText } = openModal(basePeer({ i1: '<b 0x01>', signatureProfile: 'dns' }));

		await fireEvent.input(getByLabelText('Имя / описание'), { target: { value: 'laptop' } });
		await fireEvent.click(getByText('Сохранить'));

		expect(api.updateSystemServerPeer).toHaveBeenCalledWith('Wireguard0', 'pk', {
			description: 'laptop',
			tunnelIP: '10.9.0.2/32',
			signature: undefined
		});
	});

	// Q30: сигнатуре негде жить без локального секрета — редактор не показываем.
	it('скрывает редактор сигнатуры у пира без локального ключа', () => {
		const { queryByText } = openModal(basePeer({ confAvailable: false }));
		expect(queryByText('Сгенерировать')).toBeNull();
	});

	it('блокирует сохранение при сигнатуре сверх лимита', () => {
		const { getByText } = openModal(basePeer({ i1: '<r 4100>' }));
		expect((getByText('Сохранить').closest('button') as HTMLButtonElement).disabled).toBe(true);
	});
});
