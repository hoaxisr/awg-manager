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
});
