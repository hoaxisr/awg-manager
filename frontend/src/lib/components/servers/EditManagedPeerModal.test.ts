import { describe, it, expect, vi } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
import EditManagedPeerModal from './EditManagedPeerModal.svelte';
import { api } from '$lib/api/client';
import type { ManagedPeer } from '$lib/types';

vi.mock('$lib/api/client', () => ({ api: { updateManagedPeer: vi.fn(), generateSignature: vi.fn(), getManagedPeerPresets: vi.fn() } }));
vi.mock('$lib/stores/notifications', () => ({ notifications: { success: vi.fn(), error: vi.fn() } }));
vi.mock('$lib/stores/servers', () => ({ servers: { applyMutationResponse: vi.fn() } }));

function basePeer(over: Partial<ManagedPeer> = {}): ManagedPeer {
	return {
		publicKey: 'pk',
		privateKey: 'sk',
		presharedKey: '',
		description: 'client',
		tunnelIP: '10.8.0.2/32',
		enabled: true,
		i1: '',
		i2: '',
		i3: '',
		i4: '',
		i5: '',
		...over,
	};
}

describe('EditManagedPeerModal', () => {
	it('disables Save when the signature exceeds MAX_SIGNATURE_CHARS', () => {
		const { getByText } = render(EditManagedPeerModal, {
			open: true,
			serverId: 'srv',
			peer: basePeer({ i1: 'x'.repeat(3501) }),
			onclose: vi.fn(),
			onUpdated: vi.fn(),
		});

		expect((getByText('Сохранить').closest('button') as HTMLButtonElement).disabled).toBe(true);
	});

	it('keeps Save enabled when the signature is within the limit', () => {
		const { getByText } = render(EditManagedPeerModal, {
			open: true,
			serverId: 'srv',
			peer: basePeer({ i1: '<r 10>' }),
			onclose: vi.fn(),
			onUpdated: vi.fn(),
		});

		expect((getByText('Сохранить').closest('button') as HTMLButtonElement).disabled).toBe(false);
	});

	it('disables Save and shows an error for a Tunnel IP without a prefix, enables on fix', async () => {
		const { getByText, getByLabelText, baseElement } = render(EditManagedPeerModal, {
			open: true,
			serverId: 'srv',
			peer: basePeer({ tunnelIP: '10.0.0.2' }),
			onclose: vi.fn(),
			onUpdated: vi.fn(),
		});

		const saveButton = () => getByText('Сохранить').closest('button') as HTMLButtonElement;
		expect(saveButton().disabled).toBe(true);
		expect(baseElement.querySelector('.field-hint.is-error')?.textContent).toMatch(/префикс/);

		await fireEvent.input(getByLabelText('Tunnel IP (CIDR)'), { target: { value: '10.0.0.2/32' } });

		expect(saveButton().disabled).toBe(false);
		expect(baseElement.querySelector('.field-hint.is-error')).toBeFalsy();
	});
});

describe('EditManagedPeerModal: сети клиента', () => {
	it('предзаполняется из пира и шлёт оба поля всегда', async () => {
		vi.mocked(api.updateManagedPeer).mockResolvedValue(
			{} as Awaited<ReturnType<typeof api.updateManagedPeer>>
		);
		const { getByText, getByLabelText } = render(EditManagedPeerModal, {
			open: true,
			serverId: 'srv',
			peer: basePeer({ clientAllowedIPs: '10.8.0.0/24', remoteSubnets: ['192.168.77.0/24'] }),
			onclose: vi.fn(),
			onUpdated: vi.fn(),
		});

		expect((getByLabelText('AllowedIPs клиента') as HTMLInputElement).value).toBe('10.8.0.0/24');
		await fireEvent.input(getByLabelText('Сети за клиентом'), { target: { value: '' } });
		await fireEvent.click(getByText('Сохранить'));

		expect(api.updateManagedPeer).toHaveBeenCalledWith(
			'srv',
			'pk',
			expect.objectContaining({ clientAllowedIPs: '10.8.0.0/24', remoteSubnets: [] })
		);
	});
});
