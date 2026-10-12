import { describe, it, expect, afterEach, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import { flushSync } from 'svelte';
import SubscriptionImportPreview from './SubscriptionImportPreview.svelte';
import SubscriptionExcludedSection from './SubscriptionExcludedSection.svelte';
import HeadersTextarea from './HeadersTextarea.svelte';
import { allHeadersPreset } from './headersParser';
import type { SubscriptionMember, SubscriptionPreviewMember } from '$lib/types';
import { locale, m } from '$lib/i18n';

vi.mock('$lib/api/client', () => ({
	api: {
		listSubscriptionHeaderProfiles: vi.fn().mockResolvedValue([]),
	},
}));

afterEach(() => {
	locale.set('ru');
	localStorage.clear();
});

const previewMembers: SubscriptionPreviewMember[] = [
	{ key: 'k-alpha', label: 'Alpha NL', protocol: 'vless', server: 'nl.example.com', port: 443, security: 'reality' },
	{ key: 'k-bravo', label: 'Bravo DE', protocol: 'trojan', server: 'de.example.com', port: 8443, security: 'tls' },
];

const excludedMembers: SubscriptionMember[] = [
	{ tag: 'tag-alpha-1234', label: 'Alpha NL', protocol: 'vless', server: 'nl.example.com', port: 443 },
];

describe('subscriptions i18n', () => {
	it('SubscriptionImportPreview: фильтр, счётчики и пустое состояние переключаются на английский', async () => {
		const { container } = render(SubscriptionImportPreview, {
			props: {
				members: previewMembers,
				excludedKeys: new Set<string>(['k-bravo']),
				ontoggle: vi.fn(),
				onselectAll: vi.fn(),
				onselectNone: vi.fn(),
			},
		});
		expect(screen.getByRole('button', { name: 'Выбрать все' })).toBeTruthy();
		expect(screen.getByPlaceholderText('Фильтр по названию или серверу')).toBeTruthy();
		expect(screen.getByText('Все (2)')).toBeTruthy();
		expect(screen.getByText('1 оставить')).toBeTruthy();
		expect(screen.getByText('1 исключить')).toBeTruthy();

		await fireEvent.input(screen.getByPlaceholderText('Фильтр по названию или серверу'), {
			target: { value: 'zzz' },
		});
		expect(container.querySelector('.empty-list')?.textContent).toBe('Нет серверов по фильтру.');

		locale.set('en');
		flushSync();

		expect(screen.getByRole('button', { name: 'Select all' })).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Clear all' })).toBeTruthy();
		expect(screen.getByPlaceholderText('Filter by name or server')).toBeTruthy();
		expect(screen.getByText('All (2)')).toBeTruthy();
		expect(screen.getByText('1 to keep')).toBeTruthy();
		expect(screen.getByText('1 to exclude')).toBeTruthy();
		expect(container.querySelector('.empty-list')?.textContent).toBe('No servers match the filter.');
	});

	it('SubscriptionExcludedSection: подсказка, кнопки и параметризованная кнопка массового возврата', async () => {
		const { container } = render(SubscriptionExcludedSection, {
			props: { members: excludedMembers, restoring: false, onrestore: vi.fn() },
		});
		expect(screen.getByText(/Эти серверы вы исключили вручную/)).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Вернуть' })).toBeTruthy();

		await fireEvent.click(container.querySelector('.excluded-sel input[type="checkbox"]') as HTMLInputElement);
		expect(screen.getByRole('button', { name: /Вернуть выбранные \(1\)/ })).toBeTruthy();

		locale.set('en');
		flushSync();

		expect(screen.getByText(/You excluded these servers manually/)).toBeTruthy();
		expect(screen.getByRole('button', { name: /Restore selected \(1\)/ })).toBeTruthy();
	});

	it('HeadersTextarea: подпись, подсказка и плейсхолдер переводятся; шаблон заголовков — функция', async () => {
		render(HeadersTextarea, { props: { value: '' } });
		expect(screen.getByText('Заголовки запроса')).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Подсказка по заголовкам' })).toBeTruthy();
		expect(screen.getByRole('textbox').getAttribute('placeholder')).toBe(
			'# Пример:\nUser-Agent: mihomo/v1.19.20',
		);
		expect(allHeadersPreset().startsWith('# Заполните только нужные строки.')).toBe(true);

		locale.set('en');
		flushSync();

		expect(screen.getByText('Request headers')).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Headers help' })).toBeTruthy();
		expect(screen.getByRole('textbox').getAttribute('placeholder')).toBe(
			'# Example:\nUser-Agent: mihomo/v1.19.20',
		);
		expect(allHeadersPreset().startsWith('# Fill in only the lines you need.')).toBe(true);
	});

	it('плюральные сообщения: русские формы и английские one/other', () => {
		expect(m.subscriptions_card_servers_count({ count: 1 })).toBe('1 сервер');
		expect(m.subscriptions_card_servers_count({ count: 3 })).toBe('3 сервера');
		expect(m.subscriptions_card_servers_count({ count: 5 })).toBe('5 серверов');
		expect(m.subscriptions_groups_meta({ mode: 'селектор', count: 21 })).toBe('селектор · 21 сервер');
		expect(m.subscriptions_page_loaded_of_total({ loaded: 3, total: 100 })).toBe('Загружено 3 из 100 серверов');

		locale.set('en');
		flushSync();

		expect(m.subscriptions_card_servers_count({ count: 1 })).toBe('1 server');
		expect(m.subscriptions_card_servers_count({ count: 5 })).toBe('5 servers');
		expect(m.subscriptions_groups_meta({ mode: 'selector', count: 2 })).toBe('selector · 2 servers');
		expect(m.subscriptions_page_loaded_of_total({ loaded: 3, total: 100 })).toBe('Loaded 3 of 100 servers');
	});
});
