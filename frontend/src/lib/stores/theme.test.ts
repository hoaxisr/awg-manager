import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { ThemePreset, ThemeSelection } from './theme';

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
	};
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

describe('resolveThemeTokens — новые пресеты', () => {
	beforeEach(() => {
		vi.stubGlobal('localStorage', createLocalStorageMock());
		installMatchMediaMock(false);
	});

	async function tokensFor(preset: ThemePreset, mode: 'dark' | 'light') {
		vi.resetModules();
		const { resolveThemeTokens, DEFAULT_CUSTOM_THEME } = await import('./theme');
		const selection: ThemeSelection = { preset, modePreference: mode, custom: DEFAULT_CUSTOM_THEME };
		return resolveThemeTokens(selection);
	}

	const expectations: Record<string, Record<'dark' | 'light', Record<string, string>>> = {
		grafit: {
			dark: {
				'--color-bg-primary': '#131315',
				'--color-bg-secondary': '#1b1b1f',
				'--color-bg-tertiary': '#26262c',
				'--color-bg-hover': '#303038',
				'--color-text-primary': '#e8e8ec',
				'--color-text-muted': '#74747f',
				'--color-accent': '#e8a33d',
				'--color-accent-hover': '#f2b657',
				'--color-accent-contrast': '#131315',
				'--color-success': '#6fb388',
				'--color-error': '#d47585',
				'--color-warning': '#d9a05b',
				'--color-info': '#7aa6c2',
				'--color-border': '#34343d',
				'--color-border-hover': '#585860',
				'--color-tunneled-row': 'rgba(232, 163, 61, 0.03)',
				'--shadow': '0 2px 8px rgba(0, 0, 0, 0.3)',
			},
			light: {
				'--color-bg-primary': '#ececee',
				'--color-bg-secondary': '#f7f7f8',
				'--color-bg-tertiary': '#dfdfe3',
				'--color-bg-hover': '#d2d2d8',
				'--color-text-primary': '#1d1d24',
				'--color-text-muted': '#6d6d78',
				'--color-accent': '#b97a1e',
				'--color-accent-hover': '#a06715',
				'--color-accent-contrast': '#131315',
				'--color-success': '#3d7d55',
				'--color-error': '#a94b5c',
				'--color-warning': '#9a742e',
				'--color-info': '#3f6f8e',
				'--color-border': '#c2c2c9',
				'--color-border-hover': '#a1a1a8',
				'--color-tunneled-row': 'rgba(185, 122, 30, 0.07)',
				'--shadow': '0 2px 10px rgba(29, 29, 36, 0.08)',
			},
		},
		sever: {
			dark: {
				'--color-bg-primary': '#14181f',
				'--color-bg-secondary': '#1c222b',
				'--color-bg-tertiary': '#272f3a',
				'--color-bg-hover': '#333d4b',
				'--color-text-primary': '#dde3ec',
				'--color-text-muted': '#6e7a8c',
				'--color-accent': '#7fb3c8',
				'--color-accent-hover': '#93c4d8',
				'--color-accent-contrast': '#14181f',
				'--color-success': '#8fb573',
				'--color-error': '#c76b74',
				'--color-warning': '#d1a15e',
				'--color-info': '#81a1c1',
				'--color-border': '#39434f',
				'--color-border-hover': '#5a636e',
				'--color-tunneled-row': 'rgba(127, 179, 200, 0.03)',
				'--shadow': '0 2px 8px rgba(0, 0, 0, 0.3)',
			},
			light: {
				'--color-bg-primary': '#e8ebef',
				'--color-bg-secondary': '#f4f6f8',
				'--color-bg-tertiary': '#d9dee5',
				'--color-bg-hover': '#cbd2db',
				'--color-text-primary': '#242b36',
				'--color-text-muted': '#6b7684',
				'--color-accent': '#3a7d99',
				'--color-accent-hover': '#2f6a83',
				'--color-accent-contrast': '#ffffff',
				'--color-success': '#4c7d3a',
				'--color-error': '#a44e58',
				'--color-warning': '#96703a',
				'--color-info': '#46698f',
				'--color-border': '#b9c1cb',
				'--color-border-hover': '#9ba3ad',
				'--color-tunneled-row': 'rgba(58, 125, 153, 0.07)',
				'--shadow': '0 2px 10px rgba(36, 43, 54, 0.08)',
			},
		},
		mokh: {
			dark: {
				'--color-bg-primary': '#171614',
				'--color-bg-secondary': '#201e1b',
				'--color-bg-tertiary': '#2b2925',
				'--color-bg-hover': '#383530',
				'--color-text-primary': '#e6e1d7',
				'--color-text-muted': '#7d776b',
				'--color-accent': '#97a97c',
				'--color-accent-hover': '#a9bb8e',
				'--color-accent-contrast': '#171614',
				'--color-success': '#6faf7f',
				'--color-error': '#c96f66',
				'--color-warning': '#cc9a4e',
				'--color-info': '#7fa6a3',
				'--color-border': '#3b3833',
				'--color-border-hover': '#5d5a54',
				'--color-tunneled-row': 'rgba(151, 169, 124, 0.03)',
				'--shadow': '0 2px 8px rgba(0, 0, 0, 0.3)',
			},
			light: {
				'--color-bg-primary': '#eceae5',
				'--color-bg-secondary': '#f6f5f1',
				'--color-bg-tertiary': '#dedbd3',
				'--color-bg-hover': '#d0ccc2',
				'--color-text-primary': '#26241f',
				'--color-text-muted': '#6f6a5f',
				'--color-accent': '#5f7440',
				'--color-accent-hover': '#4e6134',
				'--color-accent-contrast': '#ffffff',
				'--color-success': '#3f7d50',
				'--color-error': '#a1493f',
				'--color-warning': '#8f6a2a',
				'--color-info': '#3f6f6b',
				'--color-border': '#c4bfb3',
				'--color-border-hover': '#a4a095',
				'--color-tunneled-row': 'rgba(95, 116, 64, 0.07)',
				'--shadow': '0 2px 10px rgba(38, 36, 31, 0.08)',
			},
		},
	};

	for (const [preset, modes] of Object.entries(expectations)) {
		for (const mode of ['dark', 'light'] as const) {
			it(`${preset} ${mode} соответствует утверждённой палитре`, async () => {
				expect(await tokensFor(preset as ThemePreset, mode)).toMatchObject(modes[mode]);
			});
		}
	}

	it('каждая карта новых пресетов содержит ровно 22 ключа', async () => {
		for (const preset of ['grafit', 'sever', 'mokh'] as const) {
			for (const mode of ['dark', 'light'] as const) {
				expect(Object.keys(await tokensFor(preset, mode))).toHaveLength(22);
			}
		}
	});
});

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
