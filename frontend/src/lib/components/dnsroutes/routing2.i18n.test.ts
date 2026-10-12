import { describe, it, expect, afterEach, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import { flushSync } from 'svelte';
import DnsRouteDomainEditor from './DnsRouteDomainEditor.svelte';
import NdmsDisclaimerBanner from './NdmsDisclaimerBanner.svelte';
import HrNeoDisabledTagsView from '../hrneo/HrNeoDisabledTagsView.svelte';
import { locale, m } from '$lib/i18n';

vi.mock('$lib/api/client', () => ({ api: {} }));

afterEach(() => {
	locale.set('ru');
	localStorage.clear();
});

describe('routing2 i18n (dnsroutes / hrneo)', () => {
	it('DnsRouteDomainEditor: счётчик записей и подсказка переключаются на английский', () => {
		render(DnsRouteDomainEditor, {
			props: { domains: ['a.com', 'b.com'], onchange: vi.fn() },
		});
		expect(screen.getByText('2 записи')).toBeTruthy();
		expect(screen.getByText('Один домен или CIDR на строку.', { exact: false })).toBeTruthy();

		locale.set('en');
		flushSync();

		expect(screen.getByText('2 entries')).toBeTruthy();
		expect(screen.getByText('One domain or CIDR per line.', { exact: false })).toBeTruthy();
		expect(screen.queryByText('2 записи')).toBeNull();
	});

	it('NdmsDisclaimerBanner: производный список ограничений реагирует на смену языка', () => {
		render(NdmsDisclaimerBanner, { props: { isOS5: true } });
		expect(screen.getByText('Ограничения DNS-маршрутизации NDMS')).toBeTruthy();
		expect(
			screen.getByText('Домены с коротким TTL могут не попадать в таблицу маршрутизации.'),
		).toBeTruthy();

		locale.set('en');
		flushSync();

		expect(screen.getByText('NDMS DNS routing limitations')).toBeTruthy();
		expect(
			screen.getByText('Domains with a short TTL may not make it into the routing table.'),
		).toBeTruthy();
	});

	it('HrNeoDisabledTagsView: плюрал в баннере и счётчик записей переключаются на английский', () => {
		const { container } = render(HrNeoDisabledTagsView, {
			props: {
				tags: [{ name: 'GOOGLE', count: 2 }],
				maxelem: 65536,
			} as never,
		});
		expect(container.textContent).toContain('HR Neo исключил 1 тег из маршрутизации');
		expect(container.textContent).toContain('2 записи');

		locale.set('en');
		flushSync();

		expect(container.textContent).toContain('HR Neo excluded 1 tag from routing');
		expect(container.textContent).toContain('2 entries');
	});

	it('plural-сообщения: русские формы грамматически верны, английские — one/other', () => {
		expect(m.dns_routes_editor_entries_count({ count: 1 })).toBe('1 запись');
		expect(m.dns_routes_editor_entries_count({ count: 3 })).toBe('3 записи');
		expect(m.dns_routes_editor_entries_count({ count: 11 })).toBe('11 записей');
		expect(m.hrneo_disabled_tags_banner({ count: 5 })).toBe(
			'HR Neo исключил 5 тегов из маршрутизации — превышают',
		);
		expect(
			m.dns_routes_edit_dedup_summary({ total: 21, exact: 1, wildcard: 3 }),
		).toBe('Убрано 21 дубль (1 точный, 3 wildcard)');
		expect(m.dns_routes_edit_total_groups({ domains: 5, groups: 2 })).toBe(
			'Итого: 5 доменов → 2 группы по 300',
		);

		locale.set('en');
		expect(m.dns_routes_editor_entries_count({ count: 1 })).toBe('1 entry');
		expect(m.dns_routes_editor_entries_count({ count: 5 })).toBe('5 entries');
		expect(m.dns_routes_edit_total_groups({ domains: 1, groups: 2 })).toBe(
			'Total: 1 domain → 2 groups of 300',
		);
	});
});
