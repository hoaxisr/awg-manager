import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/svelte';
import DNSChainPresetCard from './DNSChainPresetCard.svelte';
import type { SingboxRouterDNSServer } from '$lib/types';

class ResizeObserverStub {
	observe(): void {}
	disconnect(): void {}
}
vi.stubGlobal('ResizeObserver', ResizeObserverStub);

const servers: SingboxRouterDNSServer[] = [
	{ tag: 'dns-direct', type: 'udp', server: '1.1.1.1' },
	{ tag: 'dns-tunnel', type: 'tls', server: 'dns.example', detour: 'tunnel' },
	{ tag: 'fake', type: 'fakeip', server: '' },
];

const baseProps = {
	servers,
	preset: { mode: '' as const },
	finalServer: 'dns-direct',
	onApply: vi.fn(),
};

describe('DNSChainPresetCard', () => {
	it('в режиме «Выкл» полей выбора серверов нет', () => {
		render(DNSChainPresetCard, { props: { ...baseProps, onApply: vi.fn() } });

		expect(screen.queryByText('Прямой DNS')).toBeNull();
		expect(screen.queryByText('DNS через туннель')).toBeNull();
	});

	it('«Отказоустойчивый» показывает серверы без списка подозрительных IP', async () => {
		render(DNSChainPresetCard, { props: { ...baseProps, onApply: vi.fn() } });

		await fireEvent.click(screen.getByText('Отказоустойчивый'));

		expect(screen.getByText('Прямой DNS')).toBeTruthy();
		expect(screen.getByText('DNS через туннель')).toBeTruthy();
		expect(screen.queryByText('Подозрительные IP (CIDR, по строке)')).toBeNull();
	});

	it('«Анти-подмена» добавляет список подозрительных IP', async () => {
		render(DNSChainPresetCard, { props: { ...baseProps, onApply: vi.fn() } });

		await fireEvent.click(screen.getByText('Анти-подмена'));

		expect(screen.getByText('Прямой DNS')).toBeTruthy();
		expect(screen.getByText('Подозрительные IP (CIDR, по строке)')).toBeTruthy();
	});

	it('в списках серверов нет fakeip-серверов, detour виден в подписи', async () => {
		render(DNSChainPresetCard, {
			props: {
				...baseProps,
				preset: { mode: 'resilient' as const, directServer: 'dns-direct', proxyServer: 'dns-tunnel' },
				onApply: vi.fn(),
			},
		});

		await fireEvent.click(screen.getByText('dns-direct'));
		const labels = screen.getAllByRole('option').map((el) => (el.textContent ?? '').trim());

		expect(labels.some((l) => l.includes('fake'))).toBe(false);
		expect(labels.some((l) => l.includes('dns-direct'))).toBe(true);
		expect(labels.some((l) => l.includes('через tunnel'))).toBe(true);
	});

	it('подсказка называет финальный сервер фолбэка', () => {
		render(DNSChainPresetCard, { props: { ...baseProps, onApply: vi.fn() } });

		expect(
			screen.getByText(/запрос уходит на финальный сервер: dns-direct/),
		).toBeTruthy();
	});

	it('в режиме FakeIP карточка задизейблена', () => {
		render(DNSChainPresetCard, {
			props: { ...baseProps, fakeipMode: true, onApply: vi.fn() },
		});

		expect(screen.getByText('Недоступно в режиме FakeIP')).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Отказоустойчивый' }).hasAttribute('disabled')).toBe(true);
		expect(screen.getByRole('button', { name: 'Применить' }).hasAttribute('disabled')).toBe(true);
	});

	it('«Применить» отдаёт выбранный пресет', async () => {
		const onApply = vi.fn().mockResolvedValue(undefined);
		render(DNSChainPresetCard, {
			props: {
				...baseProps,
				preset: { mode: 'antipoison' as const, directServer: 'dns-direct', proxyServer: 'dns-tunnel' },
				onApply,
			},
		});

		await fireEvent.input(screen.getByLabelText('Подозрительные IP (CIDR, по строке)'), {
			target: { value: '0.0.0.0/32\n127.0.0.0/8' },
		});
		await fireEvent.click(screen.getByRole('button', { name: 'Применить' }));

		expect(onApply).toHaveBeenCalledWith({
			mode: 'antipoison',
			directServer: 'dns-direct',
			proxyServer: 'dns-tunnel',
			poisonCidrs: ['0.0.0.0/32', '127.0.0.0/8'],
		});
	});

	it('выключение пресета не требует выбранных серверов', async () => {
		const onApply = vi.fn().mockResolvedValue(undefined);
		render(DNSChainPresetCard, {
			props: {
				...baseProps,
				preset: { mode: 'resilient' as const, directServer: 'dns-direct', proxyServer: 'dns-tunnel' },
				onApply,
			},
		});

		await fireEvent.click(screen.getByText('Выкл'));
		await fireEvent.click(screen.getByRole('button', { name: 'Применить' }));

		expect(onApply).toHaveBeenCalledWith({ mode: '' });
	});
});
