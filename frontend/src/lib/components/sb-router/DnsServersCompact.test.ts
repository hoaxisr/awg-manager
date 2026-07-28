import { describe, expect, it, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import DnsServersCompact from './DnsServersCompact.svelte';
import type { SingboxRouterDNSRule } from '$lib/types';

const rules: SingboxRouterDNSRule[] = [
	{ domain: ['a.com'], action: 'route', server: 'dns-direct' },
	{ action: 'evaluate', server: 'dns-direct', tag: 'awgm-dns-rd' },
];

describe('DnsServersCompact — managed-правила пресета', () => {
	it('помечает строку бейджем и прячет edit/delete', () => {
		render(DnsServersCompact, {
			props: {
				servers: [],
				rules,
				onEditServer: vi.fn(),
				onEditRule: vi.fn(),
				onDeleteRule: vi.fn(),
			},
		});

		expect(screen.getByText('пресет')).toBeTruthy();
		expect(screen.getByLabelText('Редактировать DNS-правило #1')).toBeTruthy();
		expect(screen.getByLabelText('Удалить DNS-правило #1')).toBeTruthy();
		expect(screen.queryByLabelText('Редактировать DNS-правило #2')).toBeNull();
		expect(screen.queryByLabelText('Удалить DNS-правило #2')).toBeNull();
	});
});
