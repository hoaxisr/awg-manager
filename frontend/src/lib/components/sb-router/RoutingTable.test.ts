import { cleanup, fireEvent, render, screen } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { m } from '$lib/i18n';
import type { SingboxRouterRule } from '$lib/types';
import RoutingTable from './RoutingTable.svelte';

const systemOwner: SingboxRouterRule = {
	action: 'sniff',
};
const userRule: SingboxRouterRule = {
	domain_suffix: ['example.com'],
	action: 'route',
	outbound: 'direct',
};

function renderTable(rules: SingboxRouterRule[], onMove = vi.fn()) {
	render(RoutingTable, {
		rules,
		outbounds: [],
		onMove,
		onEdit: vi.fn(),
		onDelete: vi.fn(),
	});
	return onMove;
}

afterEach(cleanup);

describe('RoutingTable rule movement', () => {
	it('disables moving the rule below a system rule upward', () => {
		renderTable([systemOwner, userRule]);
		const up = screen.getByRole('button', { name: m.sb_router_table_move_up_aria({ n: 1 }) });

		expect(up.hasAttribute('disabled')).toBe(true);
	});

	it('disables moving the rule above a system rule downward', () => {
		renderTable([userRule, systemOwner]);
		const down = screen.getByRole('button', { name: m.sb_router_table_move_down_aria({ n: 0 }) });

		expect(down.hasAttribute('disabled')).toBe(true);
	});

	it('disables moves across a private bypass with a custom outbound', () => {
		const privateRule: SingboxRouterRule = { ip_is_private: true, outbound: 'lan-custom' };
		renderTable([privateRule, userRule]);
		const up = screen.getByRole('button', { name: m.sb_router_table_move_up_aria({ n: 1 }) });
		expect(up.hasAttribute('disabled')).toBe(true);
	});

	it('still allows movement between neighboring user rules', async () => {
		const otherUserRule: SingboxRouterRule = {
			domain_suffix: ['other.example'],
			action: 'route',
			outbound: 'direct',
		};
		const onMove = renderTable([userRule, otherUserRule]);
		const down = screen.getByRole('button', { name: m.sb_router_table_move_down_aria({ n: 0 }) });

		expect(down.hasAttribute('disabled')).toBe(false);
		await fireEvent.click(down);
		expect(onMove).toHaveBeenCalledWith(0, 'down');
	});
});
