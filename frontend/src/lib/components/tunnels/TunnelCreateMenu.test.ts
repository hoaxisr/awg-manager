import { describe, it, expect, vi } from 'vitest';
import { render, fireEvent, screen } from '@testing-library/svelte';
import { createRawSnippet } from 'svelte';
import TunnelCreateMenu from './TunnelCreateMenu.svelte';

const triggerIcon = createRawSnippet(() => ({ render: () => '<span></span>' }));

async function openMenu(): Promise<void> {
	await fireEvent.click(screen.getByRole('button', { name: /Создать/ }));
}

describe('TunnelCreateMenu', () => {
	it('пункт Endpoint показан только когда передан onAwg3', async () => {
		const { unmount } = render(TunnelCreateMenu, {
			props: { onAwg: vi.fn(), triggerIcon },
		});
		await openMenu();
		expect(screen.queryByText('Endpoint')).toBeNull();
		unmount();

		render(TunnelCreateMenu, {
			props: { onAwg: vi.fn(), onAwg3: vi.fn(), triggerIcon },
		});
		await openMenu();
		expect(screen.getByText('Endpoint')).toBeTruthy();
	});

	it('пункт Endpoint скрыт вместе с остальными sing-box пунктами', async () => {
		render(TunnelCreateMenu, {
			props: { onAwg: vi.fn(), onAwg3: vi.fn(), showSingbox: false, triggerIcon },
		});
		await openMenu();
		expect(screen.queryByText('Endpoint')).toBeNull();
		expect(screen.getByText('AmneziaWG туннель')).toBeTruthy();
	});

	it('клик по пункту Endpoint вызывает колбэк', async () => {
		const onAwg3 = vi.fn();
		render(TunnelCreateMenu, { props: { onAwg: vi.fn(), onAwg3, triggerIcon } });
		await openMenu();
		await fireEvent.click(screen.getByText('Endpoint'));
		expect(onAwg3).toHaveBeenCalledOnce();
	});
});
