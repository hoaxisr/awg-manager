import { beforeEach, describe, expect, it, vi } from 'vitest';

type MediaQueryEntry = {
	mql: MediaQueryList;
	handlers: Set<(event: MediaQueryListEvent) => void>;
};

let prefersLight = false;
let mediaQueries: MediaQueryEntry[] = [];

function createLocalStorageMock(): Storage {
	const data = new Map<string, string>();
	return {
		get length() {
			return data.size;
		},
		clear: () => {
			data.clear();
		},
		getItem: (key: string) => data.get(key) ?? null,
		key: (index: number) => Array.from(data.keys())[index] ?? null,
		removeItem: (key: string) => {
			data.delete(key);
		},
		setItem: (key: string, value: string) => {
			data.set(key, value);
		},
	} as Storage;
}

function installMatchMediaMock(initialPrefersLight: boolean): void {
	prefersLight = initialPrefersLight;
	mediaQueries = [];

	Object.defineProperty(window, 'matchMedia', {
		writable: true,
		configurable: true,
		value: vi.fn((query: string) => {
			const handlers = new Set<(event: MediaQueryListEvent) => void>();
			const mql = {
				matches: prefersLight,
				media: query,
				onchange: null,
				addEventListener: (_type: string, listener: (event: MediaQueryListEvent) => void) => {
					handlers.add(listener);
				},
				removeEventListener: (_type: string, listener: (event: MediaQueryListEvent) => void) => {
					handlers.delete(listener);
				},
				addListener: (listener: (event: MediaQueryListEvent) => void) => {
					handlers.add(listener);
				},
				removeListener: (listener: (event: MediaQueryListEvent) => void) => {
					handlers.delete(listener);
				},
				dispatchEvent: () => true,
			} as MediaQueryList;

			mediaQueries.push({ mql, handlers });
			return mql;
		}),
	});
}

function emitSystemPreference(prefersLightNow: boolean): void {
	prefersLight = prefersLightNow;
	for (const { mql, handlers } of mediaQueries) {
		(mql as { matches: boolean }).matches = prefersLightNow;
		const event = { matches: prefersLightNow, media: mql.media } as MediaQueryListEvent;
		for (const handler of handlers) {
			handler(event);
		}
	}
}

async function initThemeStore() {
	vi.resetModules();
	const module = await import('./theme');
	module.theme.init();
	return module;
}

function latestState(spy: ReturnType<typeof vi.fn>) {
	const current = spy.mock.calls.at(-1)?.[0] as
		| {
			preset: string;
			modePreference: string;
			legacyMode: string;
			mode: string;
		}
		| undefined;
	if (!current) throw new Error('Theme state is missing');
	return current;
}

describe('theme store system mode', () => {
	beforeEach(() => {
		vi.stubGlobal('localStorage', createLocalStorageMock());
		installMatchMediaMock(false);
		document.head.innerHTML = `
			<meta name="theme-color" content="#1a1b26" />
			<meta name="apple-mobile-web-app-status-bar-style" content="black" />
		`;
	});

	it('uses system mode by default when storage is empty', async () => {
		installMatchMediaMock(true);
		const { theme } = await initThemeStore();
		const state = vi.fn();
		const unsub = theme.subscribe(state);
		const current = latestState(state);

		expect(current.modePreference).toBe('system');
		expect(current.legacyMode).toBe('light');
		expect(current.mode).toBe('light');

		unsub();
	});

	it('migrates legacy string/object storage to explicit dark/light mode', async () => {
		localStorage.setItem('awg-manager-theme', 'dark');
		let module = await initThemeStore();
		let state = vi.fn();
		let unsub = module.theme.subscribe(state);
		let current = latestState(state);

		expect(current.modePreference).toBe('dark');
		expect(current.legacyMode).toBe('dark');

		unsub();

		localStorage.setItem(
			'awg-manager-theme',
			JSON.stringify({
				preset: 'neo',
				legacyMode: 'light',
				custom: { accent: '#123456', background: '#111111', text: '#eeeeee' },
			}),
		);
		module = await initThemeStore();
		state = vi.fn();
		unsub = module.theme.subscribe(state);
		current = latestState(state);

		expect(current.preset).toBe('neo');
		expect(current.modePreference).toBe('light');
		expect(current.legacyMode).toBe('light');

		unsub();
	});

	it('follows system changes in realtime when modePreference is system', async () => {
		const { theme } = await initThemeStore();
		const state = vi.fn();
		const unsub = theme.subscribe(state);

		let current = latestState(state);
		expect(current.modePreference).toBe('system');
		expect(current.legacyMode).toBe('dark');

		emitSystemPreference(true);
		current = latestState(state);
		expect(current.modePreference).toBe('system');
		expect(current.legacyMode).toBe('light');
		expect(current.mode).toBe('light');

		unsub();
	});

	it('ignores system changes when explicit dark/light is selected', async () => {
		localStorage.setItem(
			'awg-manager-theme',
			JSON.stringify({
				preset: 'legacy',
				modePreference: 'dark',
				custom: { accent: '#8b5cf6', background: '#111827', text: '#f8fafc' },
			}),
		);
		const { theme } = await initThemeStore();
		const state = vi.fn();
		const unsub = theme.subscribe(state);

		let current = latestState(state);
		expect(current.modePreference).toBe('dark');
		expect(current.legacyMode).toBe('dark');

		emitSystemPreference(true);
		current = latestState(state);
		expect(current.modePreference).toBe('dark');
		expect(current.legacyMode).toBe('dark');
		expect(current.mode).toBe('dark');

		unsub();
	});

	it('updates theme-color and apple status-bar metadata for each active theme', async () => {
		const { theme } = await initThemeStore();
		const state = vi.fn();
		const unsub = theme.subscribe(state);

		const themeMeta = document.querySelector('meta[name="theme-color"]');
		const appleMeta = document.querySelector('meta[name="apple-mobile-web-app-status-bar-style"]');

		expect(themeMeta?.getAttribute('content')).toBe('#16161e');
		expect(appleMeta?.getAttribute('content')).toBe('black');

		theme.setPreset('mint');
		expect(themeMeta?.getAttribute('content')).toBe('#3b4252');
		expect(appleMeta?.getAttribute('content')).toBe('black');

		theme.setMode('light');
		expect(themeMeta?.getAttribute('content')).toBe('#f0f0f3');
		expect(appleMeta?.getAttribute('content')).toBe('default');

		unsub();
	});
});
