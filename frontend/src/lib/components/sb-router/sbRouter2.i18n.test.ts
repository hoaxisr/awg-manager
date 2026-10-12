import { describe, it, expect, afterEach, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import { flushSync } from 'svelte';
import { locale } from '$lib/i18n';

vi.mock('$lib/api/client', () => ({
	api: new Proxy({}, { get: () => vi.fn().mockResolvedValue([]) }),
}));

import EmptyHero from './EmptyHero.svelte';
import BulkSelectBar from './BulkSelectBar.svelte';
import {
	buildOutboundOptions,
	buildDownloadDetourOptions,
	outboundGroupLabel,
} from '$lib/components/routing/singboxRouter/outboundOptions';
import type { AWGTagInfo } from '$lib/types';

class ResizeObserverStub {
	observe(): void {}
	unobserve(): void {}
	disconnect(): void {}
}

afterEach(() => {
	vi.unstubAllGlobals();
	locale.set('ru');
	localStorage.clear();
});

const awg: AWGTagInfo[] = [{ tag: 'awg-awg10', label: 'DE', kind: 'managed', iface: 'opkgtun10' }];

describe('sb-router i18n (правила, мастер, каталоги)', () => {
	it('EmptyHero: заголовок и описание переключаются на английский', () => {
		render(EmptyHero);
		expect(screen.getByText('Не настроен')).toBeTruthy();
		expect(screen.getByText(/Направляйте трафик через VPN-туннели/)).toBeTruthy();

		locale.set('en');
		flushSync();

		expect(screen.getByText('Not configured')).toBeTruthy();
		expect(screen.getByText(/Route traffic through VPN tunnels/)).toBeTruthy();
		expect(screen.queryByText('Не настроен')).toBeNull();
	});

	it('BulkSelectBar: подписи по умолчанию и подписи групп outbound следуют за языком', async () => {
		vi.stubGlobal('ResizeObserver', ResizeObserverStub);
		// Опции собираются один раз (как в derived-сторе), язык меняется после.
		const groups = buildOutboundOptions(awg, null, null, true);
		const options = buildDownloadDetourOptions(groups, '');
		render(BulkSelectBar, {
			props: { count: 2, options, onapply: vi.fn(), oncancel: vi.fn() },
		});
		expect(screen.getByText('2 выбрано')).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Применить' })).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Отмена' })).toBeTruthy();

		await fireEvent.click(screen.getByRole('button', { expanded: false }));
		expect(screen.getByText('AWG туннели')).toBeTruthy();

		locale.set('en');
		flushSync();

		expect(screen.getByText('2 selected')).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Apply' })).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Cancel' })).toBeTruthy();
	});

	it('группы outbound: код группы стабилен, подпись и «direct» следуют за языком', () => {
		const groups = buildOutboundOptions(awg, null, null, true);
		expect(groups.map((g) => g.id)).toEqual(['special', 'awg']);
		expect(groups.map((g) => outboundGroupLabel(g.id))).toEqual(['Специальные', 'AWG туннели']);
		expect(groups[0].items[0].label).toBe('direct (мимо VPN)');
		expect(buildDownloadDetourOptions(groups, 'x').map((o) => o.group)).toEqual([
			undefined,
			'Специальные',
			'AWG туннели',
		]);

		locale.set('en');

		// Тот же собранный список: коды не изменились, подписи пересчитаны.
		expect(groups.map((g) => g.id)).toEqual(['special', 'awg']);
		expect(groups.map((g) => outboundGroupLabel(g.id))).toEqual(['Special', 'AWG tunnels']);
		expect(groups[0].items[0].label).toBe('direct (bypass VPN)');
		expect(buildDownloadDetourOptions(groups, 'x').map((o) => o.group)).toEqual([
			undefined,
			'Special',
			'AWG tunnels',
		]);
	});
});
