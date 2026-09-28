import { describe, expect, it, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import HrNeoEditModal from './HrNeoEditModal.svelte';
import type { DnsRoute, RoutingTunnel } from '$lib/types';

// Панель Dropdown читает ResizeObserver при открытии; в jsdom его нет.
vi.hoisted(() => {
	Object.defineProperty(globalThis, 'ResizeObserver', {
		writable: true,
		configurable: true,
		value: class {
			observe() {}
			unobserve() {}
			disconnect() {}
		},
	});
});

const tunnels: RoutingTunnel[] = [
	{ id: 'awg10', name: 'Мой', iface: 'nwg0', type: 'managed', status: 'running', available: true },
	{ id: 'system:Wireguard2', name: 'Wireguard2', iface: 'Wireguard2', type: 'system', status: 'up', available: true },
];

// Цель nwg1 не сопоставилась ни с одной записью каталога (например, карта
// system-выходов пуста из-за сбоя NDMS).
const rule: DnsRoute = {
	id: 'hr-yt',
	name: 'yt',
	domains: ['youtube.com'],
	manualDomains: ['youtube.com'],
	routes: [{ tunnelId: 'nwg1', interface: 'nwg1', fallback: '' }],
	enabled: true,
	createdAt: '',
	updatedAt: '',
	backend: 'hydraroute',
	hrRouteMode: 'interface',
};

function openModal(onsave = vi.fn()) {
	render(HrNeoEditModal, {
		props: {
			open: true, rule, tunnels, policies: [], policyInterfaces: [],
			geositeFiles: [], geoipFiles: [], maxelem: 0, saving: false,
			onsave, onclose: vi.fn(),
		},
	});
	return onsave;
}

describe('HrNeoEditModal: цель не найдена в каталоге', () => {
	it('не подставляет чужой туннель и держит сохранение до выбора', async () => {
		const onsave = openModal();

		expect(screen.getByText(/nwg1 \(не найдена в списке\)/)).toBeTruthy();
		expect(screen.getByText('— выбрать —')).toBeTruthy();
		const save = screen.getByRole<HTMLButtonElement>('button', { name: 'Сохранить' });
		expect(save.disabled).toBe(true);

		await fireEvent.click(screen.getByText('— выбрать —'));
		await fireEvent.click(screen.getByRole('option', { name: /Wireguard2/ }));

		expect(screen.queryByText(/не найдена в списке/)).toBeNull();
		expect(save.disabled).toBe(false);
		await fireEvent.click(save);
		expect(onsave).toHaveBeenCalledWith(
			expect.objectContaining({ routes: [{ tunnelId: 'system:Wireguard2', interface: '', fallback: '' }] }),
		);
	});
});

function openCreate(initialTarget: { kind: 'interface'; name: string; tunnelId?: string }, onsave = vi.fn()) {
	const { container } = render(HrNeoEditModal, {
		props: {
			open: true, rule: null, tunnels, policies: [], policyInterfaces: [],
			geositeFiles: [], geoipFiles: [], maxelem: 0, saving: false,
			initialTarget, onsave, onclose: vi.fn(),
		},
	});
	return { onsave, container };
}

describe('HrNeoEditModal: создание из сайдбара с незнакомой целью', () => {
	it('не подставляет первый туннель, держит сохранение до выбора', async () => {
		const { onsave } = openCreate({ kind: 'interface', name: 'nwg9' });

		expect(screen.getByText(/nwg9 \(не найдена в списке\)/)).toBeTruthy();
		await fireEvent.input(screen.getByLabelText('Название'), { target: { value: 'yt' } });
		const domains = document.querySelector<HTMLTextAreaElement>('textarea')!;
		await fireEvent.input(domains, { target: { value: 'youtube.com' } });
		const save = screen.getByRole<HTMLButtonElement>('button', { name: 'Сохранить' });
		expect(save.disabled).toBe(true);

		await fireEvent.click(screen.getByText('— выбрать —'));
		await fireEvent.click(screen.getByRole('option', { name: /Wireguard2/ }));
		expect(save.disabled).toBe(false);
		await fireEvent.click(save);
		expect(onsave).toHaveBeenCalledWith(
			expect.objectContaining({ routes: [{ tunnelId: 'system:Wireguard2', interface: '', fallback: '' }] }),
		);
	});
});

describe('HrNeoEditModal: у правила нет цели', () => {
	it('показывает причину, а не молча выключенное сохранение', () => {
		render(HrNeoEditModal, {
			props: {
				open: true, rule: { ...rule, routes: [{ tunnelId: '', interface: '', fallback: '' }] },
				tunnels, policies: [], policyInterfaces: [],
				geositeFiles: [], geoipFiles: [], maxelem: 0, saving: false,
				onsave: vi.fn(), onclose: vi.fn(),
			},
		});
		expect(screen.getByText(/Цель правила не задана — выберите туннель/)).toBeTruthy();
		expect(screen.getByRole<HTMLButtonElement>('button', { name: 'Сохранить' }).disabled).toBe(true);
	});
});
