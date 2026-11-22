import { describe, it, expect, vi } from 'vitest';
import { render, fireEvent, screen } from '@testing-library/svelte';
import { createRawSnippet } from 'svelte';
import TunnelCreateMenu from './TunnelCreateMenu.svelte';

const triggerIcon = createRawSnippet(() => ({ render: () => '<span></span>' }));

async function openMenu(): Promise<void> {
	await fireEvent.click(screen.getByRole('button', { name: /Создать/ }));
}

describe('TunnelCreateMenu', () => {
	it('пункт AWG3 показан только когда передан onAwg3', async () => {
		const { unmount } = render(TunnelCreateMenu, {
			props: { onAwg: vi.fn(), triggerIcon },
		});
		await openMenu();
		expect(screen.queryByText('AWG3 endpoint')).toBeNull();
		unmount();

		render(TunnelCreateMenu, {
			props: { onAwg: vi.fn(), onAwg3: vi.fn(), triggerIcon },
		});
		await openMenu();
		expect(screen.getByText('AWG3 endpoint')).toBeTruthy();
	});

	it('пункт AWG3 скрыт вместе с остальными sing-box пунктами', async () => {
		render(TunnelCreateMenu, {
			props: { onAwg: vi.fn(), onAwg3: vi.fn(), showSingbox: false, triggerIcon },
		});
		await openMenu();
		expect(screen.queryByText('AWG3 endpoint')).toBeNull();
		expect(screen.getByText('AmneziaWG туннель')).toBeTruthy();
	});

	it('клик по пункту AWG3 вызывает колбэк', async () => {
		const onAwg3 = vi.fn();
		render(TunnelCreateMenu, { props: { onAwg: vi.fn(), onAwg3, triggerIcon } });
		await openMenu();
		await fireEvent.click(screen.getByText('AWG3 endpoint'));
		expect(onAwg3).toHaveBeenCalledOnce();
	});

	it('пункты FIRC скрыты без showFirc=true и показаны с колбэками при showFirc=true', async () => {
		const { unmount } = render(TunnelCreateMenu, {
			props: {
				onAwg: vi.fn(),
				onFircTunnel: vi.fn(),
				onFircSubscription: vi.fn(),
				triggerIcon,
			},
		});
		await openMenu();
		expect(screen.queryByText('Туннель FIRC')).toBeNull();
		expect(screen.queryByText('Подписка FIRC')).toBeNull();
		unmount();

		const onFircTunnel = vi.fn();
		const onFircSubscription = vi.fn();
		render(TunnelCreateMenu, {
			props: {
				onAwg: vi.fn(),
				showFirc: true,
				onFircTunnel,
				onFircSubscription,
				triggerIcon,
			},
		});
		await openMenu();
		expect(screen.getByText('Туннель FIRC')).toBeTruthy();
		expect(screen.getByText('Подписка FIRC')).toBeTruthy();

		await fireEvent.click(screen.getByText('Туннель FIRC'));
		expect(onFircTunnel).toHaveBeenCalledOnce();

		await openMenu();
		await fireEvent.click(screen.getByText('Подписка FIRC'));
		expect(onFircSubscription).toHaveBeenCalledOnce();
	});

	it('при showFirc=true и showSingbox=false рендерятся только пункты AWG и FIRC', async () => {
		render(TunnelCreateMenu, {
			props: {
				onAwg: vi.fn(),
				onSingboxSingle: vi.fn(),
				onAwg3: vi.fn(),
				showSingbox: false,
				showFirc: true,
				onFircTunnel: vi.fn(),
				onFircSubscription: vi.fn(),
				triggerIcon,
			},
		});
		await openMenu();
		expect(screen.getByText('AmneziaWG туннель')).toBeTruthy();
		expect(screen.getByText('Туннель FIRC')).toBeTruthy();
		expect(screen.getByText('Подписка FIRC')).toBeTruthy();
		expect(screen.queryByText('Один сервер')).toBeNull();
		expect(screen.queryByText('AWG3 endpoint')).toBeNull();
	});

	it('при showFirc=true, showSingbox=true и fircFirst=true пункты FIRC идут перед sing-box', async () => {
		render(TunnelCreateMenu, {
			props: {
				onAwg: vi.fn(),
				onSingboxSingle: vi.fn(),
				showSingbox: true,
				showFirc: true,
				fircFirst: true,
				onFircTunnel: vi.fn(),
				onFircSubscription: vi.fn(),
				triggerIcon,
			},
		});
		await openMenu();
		const items = screen.getAllByRole('menuitem').map((el) => el.textContent ?? '');
		const fircIdx = items.findIndex((text) => text.includes('Туннель FIRC'));
		const sbIdx = items.findIndex((text) => text.includes('Один сервер'));
		expect(fircIdx).toBeGreaterThan(0);
		expect(sbIdx).toBeGreaterThan(fircIdx);
	});
});
