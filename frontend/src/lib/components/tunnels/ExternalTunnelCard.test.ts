import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/svelte';
import ExternalTunnelCard from './ExternalTunnelCard.svelte';
import type { ExternalTunnel } from '$lib/types';

const foreign: ExternalTunnel = {
	interfaceName: 'opkgtun7', tunnelNumber: 7, isAWG: false, rxBytes: 0, txBytes: 0,
	description: 'csqtt', foreign: true, removable: false,
};

describe('ExternalTunnelCard — сторонний', () => {
	it('shows badge and unmark, hides delete', async () => {
		const onunmark = vi.fn();
		render(ExternalTunnelCard, { tunnel: foreign, onunmark, ondelete: vi.fn() });
		expect(screen.getByText('сторонний')).toBeTruthy();
		expect(screen.queryByText('Удалить')).toBeNull();
		await fireEvent.click(screen.getByText('Снять отметку'));
		expect(onunmark).toHaveBeenCalledWith('opkgtun7');
	});

	// «Удалить» прячется по отметке, а не по removable: сервер мог бы и
	// принять удаление, но отмеченный интерфейс принадлежит другой программе.
	it('hides delete even when removable, in every view', () => {
		for (const view of ['cards', 'compact', 'list'] as const) {
			const { unmount } = render(ExternalTunnelCard, {
				tunnel: { ...foreign, removable: true }, view, onunmark: vi.fn(), ondelete: vi.fn(),
			});
			expect(screen.getByText('сторонний')).toBeTruthy();
			expect(screen.getByText('Снять отметку')).toBeTruthy();
			expect(screen.queryByText('Удалить')).toBeNull();
			unmount();
		}
	});

	// Сервер отказывает в приёме отмеченного (ErrAdoptForeign) — кнопку
	// не показываем, даже если интерфейс AWG.
	it('hides adopt for marked AWG, in every view', () => {
		for (const view of ['cards', 'compact', 'list'] as const) {
			const { unmount } = render(ExternalTunnelCard, {
				tunnel: { ...foreign, isAWG: true }, view, onadopt: vi.fn(), onunmark: vi.fn(),
			});
			expect(screen.queryByText('Взять под управление')).toBeNull();
			unmount();
		}
	});

	it('unmarked AWG keeps adopt', () => {
		render(ExternalTunnelCard, { tunnel: { ...foreign, isAWG: true, foreign: false }, onadopt: vi.fn() });
		expect(screen.getByText('Взять под управление')).toBeTruthy();
	});

	it('unmarked removable tunnel keeps delete, no badge', () => {
		render(ExternalTunnelCard, { tunnel: { ...foreign, foreign: false, removable: true }, ondelete: vi.fn() });
		expect(screen.getByText('Удалить')).toBeTruthy();
		expect(screen.queryByText('сторонний')).toBeNull();
		expect(screen.queryByText('Снять отметку')).toBeNull();
	});
});
