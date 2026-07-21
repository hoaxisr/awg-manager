import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/svelte';
import DNSServerEditModal from './DNSServerEditModal.svelte';

class ResizeObserverStub {
	observe(): void {}
	disconnect(): void {}
}
vi.stubGlobal('ResizeObserver', ResizeObserverStub);

const baseProps = {
	servers: [],
	outboundOptions: [],
	onClose: vi.fn(),
};

describe('DNSServerEditModal', () => {
	it('shows TLS controls only for TLS-capable DNS types', async () => {
		render(DNSServerEditModal, {
			props: { ...baseProps, onSave: vi.fn() },
		});
		expect(screen.queryByText('Server name (SNI)')).toBeNull();

		await fireEvent.click(screen.getByText('UDP (обычный DNS)'));
		await fireEvent.click(screen.getByText('DoT (DNS over TLS)'));
		expect(screen.getByText('Server name (SNI)')).toBeTruthy();
	});

	it('resets non-tag fields when type changes and serializes TLS lists', async () => {
		const onSave = vi.fn().mockResolvedValue(undefined);
		render(DNSServerEditModal, {
			props: {
				...baseProps,
				server: {
					tag: 'dns', type: 'udp', server: '1.1.1.1', server_port: 53, path: '/old',
					detour: 'tunnel', domain_strategy: 'ipv4_only',
				},
				onSave,
			},
		});

		await fireEvent.click(screen.getByText('UDP (обычный DNS)'));
		await fireEvent.click(screen.getByText('DoT (DNS over TLS)'));
		expect(screen.getByDisplayValue('dns')).toBeTruthy();
		const cloudflareInputs = screen.getAllByPlaceholderText<HTMLInputElement>('cloudflare-dns.com');
		expect(cloudflareInputs[0].value).toBe('');

		await fireEvent.input(cloudflareInputs[0], { target: { value: 'dns.example' } });
		await fireEvent.input(screen.getByPlaceholderText('h2, http/1.1'), { target: { value: 'h2,\nh3' } });
		await fireEvent.input(screen.getByPlaceholderText('base64 pin'), { target: { value: 'pin-a, pin-b' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Сохранить' }));

		expect(onSave).toHaveBeenCalledWith({
			tag: 'dns', type: 'tls', server: 'dns.example',
			tls: { alpn: ['h2', 'h3'], certificate_public_key_sha256: ['pin-a', 'pin-b'] },
		});
	});
});
