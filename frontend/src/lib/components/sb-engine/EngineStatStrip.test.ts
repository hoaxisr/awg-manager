import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { render } from '@testing-library/svelte';
import { tick } from 'svelte';
import EngineStatStrip from './EngineStatStrip.svelte';
import { singboxRouter } from '$lib/stores/singboxRouter';
import { singboxMemory } from '$lib/stores/singboxMemory';
import { singboxTrafficTotals } from '$lib/stores/singbox';
import type { SingboxRouterSettings, SingboxRouterStatus } from '$lib/types';

// Живые соединения приходят по Clash-WS; в тесте подменяем оба стора, чтобы
// проверять и число, и признак протухшего потока без сокета.
// vi.hoisted поднимается выше импортов файла, поэтому writable тянем внутри.
const { snapshotStore, wsStatusStore } = await vi.hoisted(async () => {
	const { writable } = await import('svelte/store');
	return {
		snapshotStore: writable({
			connections: [],
			downloadTotal: 0,
			uploadTotal: 0,
			connectionsTotal: 0,
		}),
		wsStatusStore: writable('open'),
	};
});
vi.mock('$lib/components/sb-router/liveConnectionsStore', () => ({
	liveConnectionsSnapshot: snapshotStore,
	liveConnectionsWsStatus: wsStatusStore,
}));

const MB = 1024 * 1024;
const GB = 1024 * MB;
const MODES = ['tproxy', 'policy-tun', 'fakeip-tun'];

function setEngine(patch: Partial<SingboxRouterStatus>, routingMode?: string): void {
	singboxRouter.applyStatus({
		enabled: false,
		installed: true,
		active: false,
		netfilterAvailable: true,
		tproxyTargetAvailable: true,
		policyName: '',
		policyExists: false,
		deviceMode: 'policy',
		snifferEnabled: false,
		deviceCount: 0,
		ruleCount: 0,
		ruleSetCount: 0,
		outboundAwgCount: 0,
		outboundCompositeCount: 0,
		final: 'direct',
		...patch,
	});
	singboxRouter.setSettings({ routingMode } as unknown as SingboxRouterSettings);
}

function setConnections(total: number): void {
	snapshotStore.set({
		connections: [],
		downloadTotal: 0,
		uploadTotal: 0,
		connectionsTotal: total,
	});
}

// Часы контролируем сами: скорость считается по дельте двух снимков, а два
// синхронных set() дают интервал 0 и честный hasRate:false.
let now = 1_000_000;

/**
 * Рендер + второй снимок счётчиков через 2 с. Порядок важен: singboxTrafficLive
 * помнит предыдущий снимок только пока у него есть подписчик, поэтому первый
 * снимок обязан достаться уже смонтированному компоненту.
 */
async function renderWithTraffic(downloadBytes: number, uploadBytes: number) {
	const utils = render(EngineStatStrip);
	now += 2000;
	singboxTrafficTotals.set({ downloadBytes, uploadBytes });
	await tick();
	return utils;
}

describe('EngineStatStrip', () => {
	beforeEach(() => {
		now = 1_000_000;
		vi.spyOn(Date, 'now').mockImplementation(() => now);
		singboxMemory.set(0);
		singboxTrafficTotals.set({ downloadBytes: 0, uploadBytes: 0 });
		wsStatusStore.set('open');
		setConnections(0);
	});

	afterEach(() => {
		vi.restoreAllMocks();
	});

	it('показывает четыре ячейки полосы', () => {
		setEngine({ enabled: true, active: true }, 'tproxy');
		const { getByText } = render(EngineStatStrip);
		for (const label of ['Память', 'Сейчас ↓', 'Соединений', 'За сессию ↓']) {
			expect(getByText(label)).toBeTruthy();
		}
	});

	// Гейт шторки (`engineActive && activeMode !== null`) знал только tproxy и
	// policy-tun — в fakeip полоса была бы пустой при живом движке.
	it.each(MODES)('при работающем движке в режиме %s числа видны', async (mode) => {
		setEngine({ enabled: true, active: true }, mode);
		singboxMemory.set(40 * MB);
		setConnections(142);
		const { getByText, queryAllByText } = await renderWithTraffic(2 * GB, 512 * MB);
		expect(getByText('40 MB')).toBeTruthy();
		expect(getByText('1.0 GB/с')).toBeTruthy();
		expect(getByText('142')).toBeTruthy();
		expect(getByText('2 GB')).toBeTruthy();
		expect(queryAllByText('—')).toHaveLength(0);
	});

	// Гейт честности: сторы держат последние значения после остановки движка —
	// SSE просто замолкает, обнулять их некому.
	it.each(MODES)('остановленный движок в режиме %s не показывает чисел', async (mode) => {
		setEngine({ enabled: true, active: false }, mode);
		singboxMemory.set(40 * MB);
		setConnections(142);
		const { queryAllByText, queryByText } = await renderWithTraffic(2 * GB, 512 * MB);
		expect(queryAllByText('—')).toHaveLength(4);
		expect(queryByText('40 MB')).toBeNull();
		expect(queryByText('142')).toBeNull();
		expect(queryByText('2 GB')).toBeNull();
	});

	it.each(MODES)('выключенный движок в режиме %s не показывает чисел', async (mode) => {
		setEngine({ enabled: false, active: false }, mode);
		singboxMemory.set(40 * MB);
		setConnections(142);
		const { queryAllByText } = await renderWithTraffic(2 * GB, 512 * MB);
		expect(queryAllByText('—')).toHaveLength(4);
	});

	// Один снимок счётчиков — интервала нет, скорость считать не из чего.
	it('до второго снимка скорость честно пустая', () => {
		setEngine({ enabled: true, active: true }, 'tproxy');
		singboxMemory.set(1024);
		const { getByText } = render(EngineStatStrip);
		expect(getByText('Сейчас ↓').previousElementSibling?.textContent).toBe('—');
		// Остальные ячейки при этом живые — пустая именно скорость.
		expect(getByText('1 KB')).toBeTruthy();
	});

	// Поток соединений рвётся независимо от движка: число оставляем последнее
	// известное, но помечаем протухшим (обнулять — мигать нулём).
	it('оборванный поток соединений помечает число протухшим', () => {
		setEngine({ enabled: true, active: true }, 'tproxy');
		setConnections(7);
		wsStatusStore.set('closed');
		const { getByText, getByTitle } = render(EngineStatStrip);
		expect(getByText('7').className).toContain('is-stale');
		expect(getByTitle(/последнее известное/)).toBeTruthy();
	});

	it('живой поток соединений протухшим не помечает', () => {
		setEngine({ enabled: true, active: true }, 'tproxy');
		setConnections(7);
		const { getByText } = render(EngineStatStrip);
		expect(getByText('7').className).not.toContain('is-stale');
	});
});
