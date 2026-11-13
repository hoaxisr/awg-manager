import { cleanup, fireEvent, render, screen } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import RulesPanel from './RulesPanel.svelte';
import { api } from '$lib/api/client';
import { singboxRouter } from '$lib/stores/singboxRouter';
import { m } from '$lib/i18n';
import type { SingboxRouterRule } from '$lib/types';

const userRule = (domain: string): SingboxRouterRule => ({
	domain_suffix: [domain],
	action: 'route',
	outbound: 'direct',
});

const systemOwner: SingboxRouterRule = {
	action: 'hijack-dns',
};

describe('RulesPanel protected drag movement', () => {
	afterEach(() => {
		cleanup();
		singboxRouter.applyRules([]);
		vi.useRealTimers();
		vi.restoreAllMocks();
		vi.unstubAllGlobals();
	});

	it.each([
		{ name: 'a system rule', systemRule: systemOwner, fromIndex: 2, targetY: 10 },
		{
			name: 'a private bypass with a custom outbound',
			systemRule: { ip_is_private: true, outbound: 'lan-custom' },
			fromIndex: 0,
			targetY: 290,
		},
	])('does not drag a user rule non-adjacently across $name', async ({ systemRule, fromIndex, targetY }) => {
		vi.stubGlobal('ResizeObserver', class {
			observe() {}
			disconnect() {}
		});
		vi.stubGlobal('requestAnimationFrame', (callback: FrameRequestCallback) => setTimeout(() => callback(0), 0));
		vi.stubGlobal('cancelAnimationFrame', (id: number) => clearTimeout(id));
		vi.spyOn(singboxRouter, 'loadAll').mockResolvedValue();
		const move = vi.spyOn(api, 'singboxRouterMoveRule').mockResolvedValue();
		singboxRouter.applyRules([userRule('first.example'), systemRule, userRule('last.example')]);
		const { container } = render(RulesPanel);
		const rect = (top: number): DOMRect => ({
			x: 0,
			y: top,
			top,
			left: 0,
			bottom: top + 100,
			right: 100,
			width: 100,
			height: 100,
			toJSON: () => ({}),
		});
		for (const [index, element] of Array.from(container.querySelectorAll<HTMLElement>('[data-rule-index]')).entries()) {
			element.getBoundingClientRect = () => rect(index * 100);
		}
		const order = String(fromIndex).padStart(2, '0');
		const handle = screen.getByRole('button', { name: m.sb_router_card_drag_aria({ n: order }) });
		const sourceY = fromIndex * 100;
		handle.getBoundingClientRect = () => rect(sourceY);

		await fireEvent.pointerDown(handle, { pointerId: 17, clientY: sourceY + 50, button: 0 });
		await fireEvent.pointerMove(window, { pointerId: 17, clientY: targetY });
		await fireEvent.pointerUp(window, { pointerId: 17, clientY: targetY });
		// A permitted drop commits after the real 360 ms animation. Waiting on the
		// API boundary also proves that no delayed request slips through.
		await vi.waitFor(() => expect(move).toHaveBeenCalled(), { timeout: 1200, interval: 10 }).catch(() => {});
		expect(move).not.toHaveBeenCalled();
	});
});
