import { describe, it, expect, afterEach, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import { flushSync } from 'svelte';
import RoutingCreateButton from './RoutingCreateButton.svelte';
import DeviceList from '../accesspolicy/DeviceList.svelte';
import ConnectionsBulkBar from './singboxRouter/ConnectionsBulkBar.svelte';
import type { Connection } from '$lib/types/singboxConnections';
import { locale, m } from '$lib/i18n';

vi.mock('$lib/api/client', () => ({ api: {} }));

afterEach(() => {
	locale.set('ru');
	localStorage.clear();
});

describe('routing i18n', () => {
	it('RoutingCreateButton: подпись по умолчанию переключается на английский', () => {
		render(RoutingCreateButton, { props: { onclick: vi.fn() } });
		expect(screen.getByText('Создать')).toBeTruthy();

		locale.set('en');
		flushSync();

		expect(screen.getByText('Create')).toBeTruthy();
		expect(screen.queryByText('Создать')).toBeNull();
	});

	it('DeviceList: заголовок, плейсхолдер и пустое состояние переключаются на английский', () => {
		const { container } = render(DeviceList, {
			props: { devices: [], currentPolicy: 'Policy0', onassign: vi.fn() },
		});
		expect(screen.getByText('Все устройства')).toBeTruthy();
		expect(screen.getByText('Нет устройств')).toBeTruthy();
		expect(container.querySelector('input[placeholder="Поиск по имени, хосту, IP..."]')).toBeTruthy();

		locale.set('en');
		flushSync();

		expect(screen.getByText('All devices')).toBeTruthy();
		expect(screen.getByText('No devices')).toBeTruthy();
		expect(container.querySelector('input[placeholder="Search by name, host, IP..."]')).toBeTruthy();
	});

	it('ConnectionsBulkBar: подпись кнопки и подтверждение с плюралом переключаются на английский', async () => {
		const visible = [
			{ id: 'c1', upload: 10, download: 20, outboundLabel: 'direct' },
			{ id: 'c2', upload: 30, download: 40, outboundLabel: 'direct' },
		] as unknown as Connection[];
		render(ConnectionsBulkBar, { props: { visible, total: 5, onConfirmKill: vi.fn() } });

		const button = screen.getByText('Закрыть видимые');
		expect(button).toBeTruthy();
		expect(screen.getByText('Видимо:', { exact: false })).toBeTruthy();

		locale.set('en');
		flushSync();

		expect(screen.getByText('Close visible')).toBeTruthy();
		expect(screen.getByText('Visible:', { exact: false })).toBeTruthy();
	});

	it('plural-сообщения: русские формы совпадают с прежними словоформами, английские — one/other', () => {
		expect(m.routing_devices_count({ count: 1 })).toBe('1 устройство');
		expect(m.routing_devices_count({ count: 3 })).toBe('3 устройства');
		expect(m.routing_devices_count({ count: 11 })).toBe('11 устройств');
		expect(m.routing_bulk_deleted_rules_partial({ ok: 2, total: 5, fail: 3 })).toBe(
			'Удалено 2 из 5 правил (3 ошибки)',
		);

		locale.set('en');
		expect(m.routing_devices_count({ count: 1 })).toBe('1 device');
		expect(m.routing_devices_count({ count: 3 })).toBe('3 devices');
		expect(m.routing_bulk_deleted_rules_partial({ ok: 2, total: 5, fail: 3 })).toBe(
			'Deleted 2 of 5 rules (3 errors)',
		);
	});
});
