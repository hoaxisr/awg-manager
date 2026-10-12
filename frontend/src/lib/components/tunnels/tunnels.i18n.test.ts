import { describe, it, expect, afterEach, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import { flushSync, createRawSnippet } from 'svelte';
import ObfuscatorParams from './ObfuscatorParams.svelte';
import TunnelTagChips from './TunnelTagChips.svelte';
import TunnelCreateMenu from './TunnelCreateMenu.svelte';
import type { TunnelObfuscator } from '$lib/types';
import { locale } from '$lib/i18n';

afterEach(() => {
	locale.set('ru');
	localStorage.clear();
});

const obfuscator: TunnelObfuscator = {
	flavor: 'phobos',
	target: 'h:1',
	key: 'k',
	masking: 'STUN',
	maxDummy: 4,
	localPort: 39000,
};

describe('tunnels i18n', () => {
	it('ObfuscatorParams: подписи и параметризованная подсказка переключаются на английский', () => {
		render(ObfuscatorParams, { props: { obfuscator } });
		expect(screen.getByLabelText('Ключ')).toBeTruthy();
		expect(screen.getByLabelText('Маскировка')).toBeTruthy();
		expect(screen.getByText(/127\.0\.0\.1:39000\), релей — на сервер ниже\./)).toBeTruthy();

		locale.set('en');
		flushSync();

		expect(screen.getByLabelText('Key')).toBeTruthy();
		expect(screen.getByLabelText('Masking')).toBeTruthy();
		expect(screen.getByLabelText('Obfuscator server (host:port)')).toBeTruthy();
		expect(screen.getByText(/127\.0\.0\.1:39000\), and the relay to the server below\./)).toBeTruthy();
	});

	it('TunnelTagChips: aria-label и title с параметром тега', () => {
		render(TunnelTagChips, { props: { tags: ['home'], onAdd: vi.fn(), onRemove: vi.fn(), onSelect: vi.fn() } });
		expect(screen.getByRole('button', { name: 'Удалить тег «home»' })).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Добавить тег' })).toBeTruthy();
		expect(screen.getByTitle('Фильтр по тегу «home»')).toBeTruthy();

		locale.set('en');
		flushSync();

		expect(screen.getByRole('button', { name: 'Remove tag "home"' })).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Add tag' })).toBeTruthy();
		expect(screen.getByTitle('Filter by tag "home"')).toBeTruthy();
	});

	it('TunnelCreateMenu: кнопка и пункты меню переводятся', async () => {
		const triggerIcon = createRawSnippet(() => ({ render: () => '<span></span>' }));
		render(TunnelCreateMenu, { props: { onAwg: vi.fn(), triggerIcon } });
		await fireEvent.click(screen.getByRole('button', { name: /Создать/ }));
		expect(screen.getByText('AmneziaWG туннель')).toBeTruthy();
		expect(screen.getByText('NativeWG или Kernel')).toBeTruthy();

		locale.set('en');
		flushSync();

		expect(screen.getByRole('button', { name: /Create/ })).toBeTruthy();
		expect(screen.getByText('AmneziaWG tunnel')).toBeTruthy();
		expect(screen.getByText('NativeWG or Kernel')).toBeTruthy();
	});
});
