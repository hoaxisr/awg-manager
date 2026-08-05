/**
 * Два слоя группы Sing-box (подэтап 5D1).
 *
 * Тонкий /sb/+layout.svelte накрывает и четыре нетрогаемые страницы — с него
 * спрос за нейтральность: только прайминг данных для бейджей, никакой разметки.
 * Слой (engine)/+layout.svelte несёт общие механики страниц движка: гейт
 * «Sing-box не установлен», баннер черновика, хост смены режима и окно
 * пересборки ipset.
 */
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import { createRawSnippet, tick } from 'svelte';
import type { Snippet } from 'svelte';

const {
	statusW,
	settingsW,
	stagingW,
	initializedW,
	systemW,
	bindLive,
	reloadStatus,
	reloadSettings,
	loadStaging,
} = vi.hoisted(() => {
	const { writable } = require('svelte/store') as typeof import('svelte/store');
	return {
		statusW: writable<unknown>(null),
		settingsW: writable<unknown>(null),
		stagingW: writable<unknown>(null),
		initializedW: writable(false),
		systemW: writable<unknown>({ data: null, status: 'idle', lastFetchedAt: 0 }),
		bindLive: vi.fn(),
		reloadStatus: vi.fn(),
		reloadSettings: vi.fn(),
		loadStaging: vi.fn(),
	};
});

vi.mock('$lib/stores/singboxRouter', () => ({
	singboxRouter: {
		status: { subscribe: statusW.subscribe },
		settings: { subscribe: settingsW.subscribe },
		staging: { subscribe: stagingW.subscribe },
		initialized: { subscribe: initializedW.subscribe },
		reloadStatus,
		reloadSettings,
		loadStaging,
		loadAll: vi.fn(),
	},
}));

vi.mock('$lib/stores/system', () => ({
	systemInfo: { subscribe: systemW.subscribe },
}));

vi.mock('$lib/components/sb-router/liveConnectionsStore', () => ({
	bindLiveConnectionsStore: bindLive,
	liveConnectionsSnapshot: {
		subscribe: (run: (v: unknown) => void) => {
			run({ connections: [], downloadTotal: 0, uploadTotal: 0, connectionsTotal: 0 });
			return () => {};
		},
	},
}));

vi.mock('$lib/api/client', () => ({
	api: new Proxy({}, { get: () => vi.fn() }),
}));

import SbLayout from './+layout.svelte';
import EngineLayout from './(engine)/+layout.svelte';
import { selectiveBypass } from '$lib/stores/selectiveBypass';
import { modeSwitch } from '$lib/stores/modeSwitch';

/** Содержимое страницы — по нему видно, дошёл ли слот до экрана. */
function pageSnippet(text = 'содержимое страницы'): Snippet {
	return createRawSnippet(() => ({
		render: () => `<p data-testid="page-content">${text}</p>`,
	}));
}

const known = (installed: boolean) => ({
	data: { singbox: { installed } },
	status: 'success',
	lastFetchedAt: 1,
});

describe('/sb/+layout — тонкий слой группы', () => {
	beforeEach(() => {
		vi.clearAllMocks();
		initializedW.set(false);
	});

	it('на холодном входе праймит статус и настройки — иначе бейджи группы пусты', () => {
		render(SbLayout, { props: { children: pageSnippet() } });
		expect(reloadStatus).toHaveBeenCalledOnce();
		expect(reloadSettings).toHaveBeenCalledOnce();
		expect(bindLive).toHaveBeenCalledOnce();
	});

	it('не перезапрашивает то, что страница движка уже загрузила', () => {
		initializedW.set(true);
		render(SbLayout, { props: { children: pageSnippet() } });
		expect(reloadStatus).not.toHaveBeenCalled();
		expect(reloadSettings).not.toHaveBeenCalled();
	});

	it('поведенчески нейтрален: рендерит слот и ничего сверх него', () => {
		const { container } = render(SbLayout, { props: { children: pageSnippet() } });
		expect(screen.getByTestId('page-content')).toBeTruthy();
		// Один элемент — сам контент страницы. Любая обёртка/баннер здесь сломала бы
		// вёрстку четырёх страниц, которые в 5D1 не трогаем.
		expect(container.querySelectorAll('*')).toHaveLength(1);
	});
});

describe('/sb/(engine)/+layout — слой страниц движка', () => {
	beforeEach(() => {
		vi.clearAllMocks();
		systemW.set({ data: null, status: 'idle', lastFetchedAt: 0 });
		stagingW.set(null);
		statusW.set({ enabled: true });
		settingsW.set({ routingMode: 'tproxy' });
		selectiveBypass.clearModalRequest();
		selectiveBypass.resetProgress();
		modeSwitch.cancel();
	});

	it('пока про систему ничего не известно, гейт не подменяет страницу', () => {
		render(EngineLayout, { props: { children: pageSnippet() } });
		expect(screen.queryByText('Sing-box не установлен')).toBeNull();
		expect(screen.getByTestId('page-content')).toBeTruthy();
	});

	it('без установленного пакета вместо страницы — гейт', () => {
		systemW.set(known(false));
		render(EngineLayout, { props: { children: pageSnippet() } });
		expect(screen.getByText('Sing-box не установлен')).toBeTruthy();
		expect(screen.queryByTestId('page-content')).toBeNull();
	});

	it('с установленным пакетом страница рендерится', () => {
		systemW.set(known(true));
		render(EngineLayout, { props: { children: pageSnippet() } });
		expect(screen.queryByText('Sing-box не установлен')).toBeNull();
		expect(screen.getByTestId('page-content')).toBeTruthy();
	});

	it('на холодном входе праймит статус черновика — его не грузит ни одна страница, кроме движка', () => {
		systemW.set(known(true));
		render(EngineLayout, { props: { children: pageSnippet() } });
		expect(loadStaging).toHaveBeenCalledOnce();
	});

	it('не перезапрашивает статус черновика, если он уже известен', () => {
		systemW.set(known(true));
		stagingW.set({ hasDraft: false });
		render(EngineLayout, { props: { children: pageSnippet() } });
		expect(loadStaging).not.toHaveBeenCalled();
	});

	it('баннер черновика показывает сам слой — на любой странице группы', () => {
		systemW.set(known(true));
		stagingW.set({ hasDraft: true });
		const { container } = render(EngineLayout, { props: { children: pageSnippet() } });
		expect(container.querySelectorAll('.staging-banner')).toHaveLength(1);
		expect(screen.getByText('Применить')).toBeTruthy();
	});

	it('окно пересборки ipset открывается по запросу из стора и переживает смену страницы', async () => {
		systemW.set(known(true));
		const { rerender } = render(EngineLayout, { props: { children: pageSnippet('первая') } });
		expect(screen.queryByText('Обновление ipset')).toBeNull();

		// Так его открывает «Применить» в баннере — с любой страницы группы.
		selectiveBypass.requestModal();
		await tick();
		expect(screen.getByText('Обновление ipset')).toBeTruthy();

		// Смена страницы внутри группы = новый слот при живом слое.
		await rerender({ children: pageSnippet('вторая') });
		expect(screen.getByText('вторая')).toBeTruthy();
		expect(screen.getByText('Обновление ipset')).toBeTruthy();
	});

	it('подтверждение смены режима показывает слой и не теряет его при смене страницы', async () => {
		systemW.set(known(true));
		const { rerender } = render(EngineLayout, { props: { children: pageSnippet('первая') } });
		expect(screen.queryByText('Включить FakeIP')).toBeNull();

		modeSwitch.request('fakeip-tun');
		await tick();
		expect(screen.getByText('Включить FakeIP')).toBeTruthy();

		await rerender({ children: pageSnippet('вторая') });
		expect(screen.getByText('вторая')).toBeTruthy();
		expect(screen.getByText('Включить FakeIP')).toBeTruthy();
	});
});
