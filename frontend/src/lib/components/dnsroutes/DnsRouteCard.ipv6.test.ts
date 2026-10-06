import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import DnsRouteCard from './DnsRouteCard.svelte';
import type { DnsRoute } from '$lib/types';

class ResizeObserverStub {
	observe() {}
	unobserve() {}
	disconnect() {}
}
vi.stubGlobal('ResizeObserver', ResizeObserverStub);

function route(skipIPv6: boolean, entries: string[]): DnsRoute {
	return {
		id: 'list_1',
		name: 'v6',
		domains: [],
		subnets: entries,
		manualDomains: entries,
		routes: [{ interface: 'OpkgTun0', tunnelId: 't1' }],
		enabled: true,
		createdAt: '',
		updatedAt: '',
		skipIPv6
	};
}

const noop = () => {};
const handlers = { ontoggle: noop, onedit: noop, ondelete: noop, onrefresh: noop };

describe('DnsRouteCard: список, который SkipIPv6 оставляет пустым', () => {
	it('помечен: на роутер он не попадает, хотя включён', () => {
		render(DnsRouteCard, { props: { route: route(true, ['2001:db8::/32']), ...handlers } });
		expect(screen.getByText('Только IPv6 — на роутер не отправлен')).toBeTruthy();
	});

	it('без флага или с IPv4-записью пометки нет', () => {
		render(DnsRouteCard, { props: { route: route(false, ['2001:db8::/32']), ...handlers } });
		render(DnsRouteCard, { props: { route: route(true, ['2001:db8::/32', '10.0.0.0/8']), ...handlers } });
		expect(screen.queryByText('Только IPv6 — на роутер не отправлен')).toBeNull();
	});
});
