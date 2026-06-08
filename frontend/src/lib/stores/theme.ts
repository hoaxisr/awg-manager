import { browser } from '$app/environment';
import { writable } from 'svelte/store';

export type ThemePreset = 'legacy' | 'neo' | 'mint' | 'custom';
export type ThemeMode = 'dark' | 'light';
export type ThemeModePreference = 'system' | ThemeMode;

export interface ThemeCustomPalette {
	accent: string;
	background: string;
	text: string;
}

export interface ThemeSelection {
	preset: ThemePreset;
	modePreference: ThemeModePreference;
	custom: ThemeCustomPalette;
}

export interface ThemeState extends ThemeSelection {
	legacyMode: ThemeMode;
	mode: ThemeMode;
	label: string;
	summary: string;
	supportsModeToggle: boolean;
}

type ThemeTokenMap = Record<string, string>;

const storageKey = 'awg-manager-theme';
const presetCycleOrder: ThemePreset[] = ['legacy', 'neo', 'mint', 'custom'];
const SYSTEM_LIGHT_MEDIA_QUERY = '(prefers-color-scheme: light)';

const faviconStorageKey = 'awg-manager-dynamic-favicon';
const faviconCacheVersion = 1;
const faviconTemplateUrl = '/favicon.svg';
const dynamicFaviconSelector = 'link[data-awgm-dynamic-favicon]';
const staticFaviconSelector = 'link[data-awgm-static-favicon]';
const faviconAccentPattern = /#7aa1f7|#7aa2f7/gi;

interface CachedDynamicFavicon {
	version: number;
	accent: string;
	href: string;
}

interface ApplyThemeStateOptions {
	refreshDynamicFavicon?: boolean;
}

let faviconSvgTemplatePromise: Promise<string> | null = null;
let dynamicFaviconUpdateSeq = 0;

export const DEFAULT_CUSTOM_THEME: ThemeCustomPalette = {
	accent: '#8b5cf6',
	background: '#111827',
	text: '#f8fafc',
};

const LEGACY_DARK_TOKENS: ThemeTokenMap = {
	'--color-accent': '#7aa2f7',
	'--color-accent-hover': '#6e8bbb',
	'--color-accent-contrast': '#0b1327',
	'--color-success': '#9ece6a',
	'--color-success-contrast': '#08130a',
	'--color-error': '#f7768e',
	'--color-error-contrast': '#ffffff',
	'--color-warning': '#e0af68',
	'--color-warning-contrast': '#1c1306',
	'--color-info': '#7dcfff',
	'--color-info-contrast': '#082f49',
	'--color-bg-primary': '#1a1b26',
	'--color-bg-secondary': '#16161e',
	'--color-bg-tertiary': '#24283b',
	'--color-bg-hover': '#292e42',
	'--color-text-primary': '#c0caf5',
	'--color-text-secondary': '#a9b1d6',
	'--color-text-muted': '#737aa2',
	'--color-border': '#3b4261',
	'--color-border-hover': '#565f89',
	'--shadow': '0 2px 8px rgba(0, 0, 0, 0.3)',
	'--color-tunneled-row': 'rgba(122, 162, 247, 0.03)',
};

const LEGACY_LIGHT_TOKENS: ThemeTokenMap = {
	'--color-accent': '#0096e1',
	'--color-accent-hover': '#1ba8ef',
	'--color-accent-contrast': '#ffffff',
	'--color-success': '#0d9488',
	'--color-success-contrast': '#f0fdfa',
	'--color-error': '#dc2626',
	'--color-error-contrast': '#fff1f2',
	'--color-warning': '#b45309',
	'--color-warning-contrast': '#fffbeb',
	'--color-info': '#0284c7',
	'--color-info-contrast': '#f0f9ff',
	'--color-bg-primary': '#f0f4f8',
	'--color-bg-secondary': '#ffffff',
	'--color-bg-tertiary': '#e4edf5',
	'--color-bg-hover': '#d6e3f0',
	'--color-text-primary': '#1a212c',
	'--color-text-secondary': '#3d4d5f',
	'--color-text-muted': '#5c6b7d',
	'--color-border': '#c5d4e4',
	'--color-border-hover': '#9eb4cc',
	'--shadow': '0 2px 10px rgba(26, 33, 44, 0.08)',
	'--color-tunneled-row': 'rgba(0, 150, 225, 0.07)',
};

const NEO_DARK_TOKENS: ThemeTokenMap = {
	'--color-accent': '#faff69',
	'--color-accent-hover': '#e6eb52',
	'--color-accent-contrast': '#0b0b0b',
	'--color-success': '#22c55e',
	'--color-success-contrast': '#052e16',
	'--color-error': '#ef4444',
	'--color-error-contrast': '#ffffff',
	'--color-warning': '#f59e0b',
	'--color-warning-contrast': '#1c1917',
	'--color-info': '#3b82f6',
	'--color-info-contrast': '#eff6ff',
	'--color-bg-primary': '#0a0a0a',
	'--color-bg-secondary': '#121212',
	'--color-bg-tertiary': '#1a1a1a',
	'--color-bg-hover': '#242424',
	'--color-text-primary': '#ffffff',
	'--color-text-secondary': '#cccccc',
	'--color-text-muted': '#888888',
	'--color-border': '#2a2a2a',
	'--color-border-hover': '#3a3a3a',
	'--shadow': '0 2px 8px rgba(0, 0, 0, 0.3)',
	'--color-tunneled-row': 'rgba(250, 255, 105, 0.03)',
};

/** Neo «светлая»: фон и текст как у тёмного Gruvbox, акцент — жёлтый Neo */
const NEO_LIGHT_TOKENS: ThemeTokenMap = {
	'--color-accent': '#faff69',
	'--color-accent-hover': '#e6eb52',
	'--color-accent-contrast': '#282828',
	'--color-success': '#b8bb26',
	'--color-success-contrast': '#282828',
	'--color-error': '#fb4934',
	'--color-error-contrast': '#282828',
	'--color-warning': '#fabd2f',
	'--color-warning-contrast': '#282828',
	'--color-info': '#83a598',
	'--color-info-contrast': '#282828',
	'--color-bg-primary': '#282828',
	'--color-bg-secondary': '#3c3836',
	'--color-bg-tertiary': '#504945',
	'--color-bg-hover': '#665c54',
	'--color-text-primary': '#ebdbb2',
	'--color-text-secondary': '#d5c4a1',
	'--color-text-muted': '#a89984',
	'--color-border': '#504945',
	'--color-border-hover': '#665c54',
	'--shadow': '0 2px 10px rgba(0, 0, 0, 0.35)',
	'--color-tunneled-row': 'rgba(250, 255, 105, 0.06)',
};

/* Отключено: слишком близко к «ещё одному синему». Раскомментируй токены + preset + ветку в resolveThemeTokens при необходимости.
const NATIVE_DARK_TOKENS: ThemeTokenMap = {
	'--color-accent': '#0096e1',
	'--color-accent-hover': '#1ba8ef',
	'--color-accent-contrast': '#ffffff',
	'--color-success': '#2dd4bf',
	'--color-success-contrast': '#042f2e',
	'--color-error': '#f87171',
	'--color-error-contrast': '#1f0a0a',
	'--color-warning': '#fbbf24',
	'--color-warning-contrast': '#1c1306',
	'--color-info': '#38bdf8',
	'--color-info-contrast': '#082f49',
	'--color-bg-primary': '#1a212c',
	'--color-bg-secondary': '#161b24',
	'--color-bg-tertiary': '#222b38',
	'--color-bg-hover': '#2c3645',
	'--color-text-primary': '#f5f8fc',
	'--color-text-secondary': '#b8c4d4',
	'--color-text-muted': '#7d8a9c',
	'--color-border': '#2f3847',
	'--color-border-hover': '#3d4a5c',
	'--shadow': '0 2px 10px rgba(0, 0, 0, 0.35)',
	'--color-tunneled-row': 'rgba(0, 150, 225, 0.06)',
};
const NATIVE_LIGHT_TOKENS: ThemeTokenMap = {
	'--color-accent': '#0096e1',
	'--color-accent-hover': '#007eb8',
	'--color-accent-contrast': '#ffffff',
	'--color-success': '#0d9488',
	'--color-success-contrast': '#f0fdfa',
	'--color-error': '#dc2626',
	'--color-error-contrast': '#fef2f2',
	'--color-warning': '#b45309',
	'--color-warning-contrast': '#fffbeb',
	'--color-info': '#0284c7',
	'--color-info-contrast': '#f0f9ff',
	'--color-bg-primary': '#eef2f6',
	'--color-bg-secondary': '#ffffff',
	'--color-bg-tertiary': '#e2e8f0',
	'--color-bg-hover': '#d8dee9',
	'--color-text-primary': '#1a212c',
	'--color-text-secondary': '#3d4a5c',
	'--color-text-muted': '#64748b',
	'--color-border': '#cbd5e1',
	'--color-border-hover': '#94a3b8',
	'--shadow': '0 2px 8px rgba(26, 33, 44, 0.08)',
	'--color-tunneled-row': 'rgba(0, 150, 225, 0.07)',
};
*/

/** Mint — Polar Night / Frost */
const MINT_DARK_TOKENS: ThemeTokenMap = {
	'--color-accent': '#88c0d0',
	'--color-accent-hover': '#9cd1df',
	'--color-accent-contrast': '#2e3440',
	'--color-success': '#a3be8c',
	'--color-success-contrast': '#2e3440',
	'--color-error': '#bf616a',
	'--color-error-contrast': '#2e3440',
	'--color-warning': '#ebcb8b',
	'--color-warning-contrast': '#3b4252',
	'--color-info': '#81a1c1',
	'--color-info-contrast': '#2e3440',
	'--color-bg-primary': '#2e3440',
	'--color-bg-secondary': '#3b4252',
	'--color-bg-tertiary': '#434c5e',
	'--color-bg-hover': '#4c566a',
	'--color-text-primary': '#eceff4',
	'--color-text-secondary': '#d8dee9',
	'--color-text-muted': '#aeb3bb',
	'--color-border': '#4c566a',
	'--color-border-hover': '#616e88',
	'--shadow': '0 2px 10px rgba(0, 0, 0, 0.28)',
	'--color-tunneled-row': 'rgba(136, 192, 208, 0.07)',
};

/** Mint light — прежний светлый AWGM Legacy: нейтральные серо-синие панели и спокойный акцент */
const MINT_LIGHT_TOKENS: ThemeTokenMap = {
	'--color-accent': '#4f6e9c',
	'--color-accent-hover': '#6082b0',
	'--color-accent-contrast': '#f8fafc',
	'--color-success': '#5b8568',
	'--color-success-contrast': '#f7fbf8',
	'--color-error': '#9a4f60',
	'--color-error-contrast': '#fff1f2',
	'--color-warning': '#a07a3f',
	'--color-warning-contrast': '#fff7ed',
	'--color-info': '#547e91',
	'--color-info-contrast': '#eff6ff',
	'--color-bg-primary': '#e9e9ed',
	'--color-bg-secondary': '#f0f0f3',
	'--color-bg-tertiary': '#d5d6db',
	'--color-bg-hover': '#cacbd2',
	'--color-text-primary': '#343b58',
	'--color-text-secondary': '#434754',
	'--color-text-muted': '#545760',
	'--color-border': '#b8b9c0',
	'--color-border-hover': '#9a9ba2',
	'--shadow': '0 2px 8px rgba(0, 0, 0, 0.1)',
	'--color-tunneled-row': 'rgba(46, 125, 233, 0.05)',
};

/*
 * Убраны отдельные пресеты (оставлен только Nord). Токены на случай возврата:
 *
 * Gruvbox dark / light, Dracula dark / light, Solarized dark / light — см. git history
 * или раскомментируй и добавь в ThemePreset / THEME_PRESETS / resolveThemeTokens.
 */

export const THEME_PRESETS = {
	legacy: {
		label: 'AWGM - Legacy',
		summary:
			'Классическая тема AWGM с глубокими тёмно-синими оттенками.',
		supportsModeToggle: true,
	},
	neo: {
		label: 'AWGM - Neo',
		summary:
			'Авторская фирменная тема AWGM в ярко-жёлтых тонах с высокой контрастностью.',
		supportsModeToggle: true,
	},
	mint: {
		label: 'AWGM - Mint',
		summary:
			'Мягкая аквамариновая палитра и нейтральная серо-синяя стилистика.',
		supportsModeToggle: true,
	},
	custom: {
		label: 'AWGM - Custom',
		summary: 'Выберите акцентный, фоновый и текстовый цвета, чтобы создать свою уникальную тему.',
		supportsModeToggle: false,
	},
} as const satisfies Record<
	ThemePreset,
	{ label: string; summary: string; supportsModeToggle: boolean }
>;

const THEME_VARIABLE_KEYS = [
	...new Set([
		...Object.keys(LEGACY_DARK_TOKENS),
		...Object.keys(LEGACY_LIGHT_TOKENS),
		...Object.keys(NEO_DARK_TOKENS),
		...Object.keys(NEO_LIGHT_TOKENS),
		...Object.keys(MINT_DARK_TOKENS),
		...Object.keys(MINT_LIGHT_TOKENS),
	]),
];

function isThemeMode(value: string | null | undefined): value is ThemeMode {
	return value === 'dark' || value === 'light';
}

function isThemeModePreference(value: string | null | undefined): value is ThemeModePreference {
	return value === 'system' || isThemeMode(value);
}

function isThemePreset(value: string | null | undefined): value is ThemePreset {
	return value === 'legacy' || value === 'neo' || value === 'mint' || value === 'custom';
}

function normalizeHexColor(value: string | null | undefined, fallback: string): string {
	if (!value) return fallback;
	const match = /^#([0-9a-f]{6})$/i.exec(value.trim());
	return match ? `#${match[1].toLowerCase()}` : fallback;
}

function getFaviconAccent(tokens: ThemeTokenMap): string {
	return normalizeHexColor(tokens['--color-accent'], DEFAULT_CUSTOM_THEME.accent);
}

function getStateAccent(state: ThemeState): string {
	return getFaviconAccent(resolveThemeTokens(selectionFromState(state)));
}

function readDynamicFaviconCache(): CachedDynamicFavicon | null {
	if (!browser) return null;

	try {
		const raw = localStorage.getItem(faviconStorageKey);
		if (!raw) return null;

		const parsed = JSON.parse(raw) as Partial<CachedDynamicFavicon> | null;
		if (
			parsed?.version !== faviconCacheVersion ||
			typeof parsed.accent !== 'string' ||
			typeof parsed.href !== 'string' ||
			!parsed.href.startsWith('data:image/svg+xml')
		) {
			return null;
		}

		const accent = normalizeHexColor(parsed.accent, '');
		if (!accent) return null;

		return {
			version: faviconCacheVersion,
			accent,
			href: parsed.href,
		};
	} catch {
		return null;
	}
}

function writeDynamicFaviconCache(accent: string, href: string): void {
	if (!browser) return;

	try {
		localStorage.setItem(
			faviconStorageKey,
			JSON.stringify({
				version: faviconCacheVersion,
				accent,
				href,
			} satisfies CachedDynamicFavicon),
		);
	} catch {
		// Ignore quota/private-mode errors; static favicon remains as fallback.
	}
}

function removeActiveFaviconLinks(): void {
	if (!browser) return;

	document
		.querySelectorAll<HTMLLinkElement>(`${staticFaviconSelector}, ${dynamicFaviconSelector}`)
		.forEach((link) => link.remove());
}

function createDynamicFaviconLink(accent: string, href: string): HTMLLinkElement {
	removeActiveFaviconLinks();

	const link = document.createElement('link');
	link.rel = 'icon';
	link.type = 'image/svg+xml';
	link.href = href;
	link.setAttribute('sizes', 'any');
	link.setAttribute('data-awgm-dynamic-favicon', '');
	link.setAttribute('data-awgm-accent', accent);
	document.head.appendChild(link);

	return link;
}

function applyDynamicFaviconHref(accent: string, href: string): void {
	if (!browser) return;

	const currentLink = document.querySelector<HTMLLinkElement>(dynamicFaviconSelector);
	const staticLinks = document.querySelectorAll<HTMLLinkElement>(staticFaviconSelector);

	if (
		currentLink?.dataset.awgmAccent === accent &&
		currentLink.getAttribute('href') === href &&
		staticLinks.length === 0
	) {
		return;
	}

	createDynamicFaviconLink(accent, href);
}

function applyCachedDynamicFavicon(tokens: ThemeTokenMap): void {
	if (!browser) return;

	const accent = getFaviconAccent(tokens);
	const cached = readDynamicFaviconCache();

	if (cached?.accent === accent) {
		applyDynamicFaviconHref(accent, cached.href);
	}
}

function loadFaviconSvgTemplate(): Promise<string> {
	if (!browser) return Promise.resolve('');

	if (!faviconSvgTemplatePromise) {
		faviconSvgTemplatePromise = fetch(faviconTemplateUrl, { cache: 'force-cache' })
			.then((response) => (response.ok ? response.text() : ''))
			.catch(() => '');
	}

	return faviconSvgTemplatePromise;
}

function buildDynamicFaviconHref(template: string, accent: string): string {
	const tintedSvg = template.replace(faviconAccentPattern, accent);
	return `data:image/svg+xml;charset=utf-8,${encodeURIComponent(tintedSvg)}`;
}

function refreshDynamicFavicon(tokens: ThemeTokenMap): void {
	if (!browser) return;

	const accent = getFaviconAccent(tokens);
	const seq = ++dynamicFaviconUpdateSeq;

	const currentLink = document.querySelector<HTMLLinkElement>(dynamicFaviconSelector);
	const staticLinks = document.querySelectorAll<HTMLLinkElement>(staticFaviconSelector);
	const cached = readDynamicFaviconCache();

	if (
		currentLink?.dataset.awgmAccent === accent &&
		cached?.accent === accent &&
		staticLinks.length === 0
	) {
		return;
	}

	if (cached?.accent === accent) {
		applyDynamicFaviconHref(accent, cached.href);
		return;
	}

	void loadFaviconSvgTemplate().then((template) => {
		if (!template || seq !== dynamicFaviconUpdateSeq) return;

		const href = buildDynamicFaviconHref(template, accent);
		writeDynamicFaviconCache(accent, href);
		applyDynamicFaviconHref(accent, href);
	});
}

function hexToRgb(hex: string): [number, number, number] {
	const normalized = normalizeHexColor(hex, '#000000').slice(1);
	return [
		Number.parseInt(normalized.slice(0, 2), 16),
		Number.parseInt(normalized.slice(2, 4), 16),
		Number.parseInt(normalized.slice(4, 6), 16),
	];
}

function rgbToHex([r, g, b]: [number, number, number]): string {
	return `#${[r, g, b]
		.map((value) => Math.max(0, Math.min(255, Math.round(value))).toString(16).padStart(2, '0'))
		.join('')}`;
}

function mixHex(from: string, to: string, amount: number): string {
	const safeAmount = Math.max(0, Math.min(1, amount));
	const [fr, fg, fb] = hexToRgb(from);
	const [tr, tg, tb] = hexToRgb(to);
	return rgbToHex([
		fr + (tr - fr) * safeAmount,
		fg + (tg - fg) * safeAmount,
		fb + (tb - fb) * safeAmount,
	] as [number, number, number]);
}

function hexToRgba(hex: string, alpha: number): string {
	const [r, g, b] = hexToRgb(hex);
	return `rgba(${r}, ${g}, ${b}, ${Math.max(0, Math.min(1, alpha))})`;
}

function channelToLinear(channel: number): number {
	const value = channel / 255;
	return value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4;
}

function relativeLuminance(hex: string): number {
	const [r, g, b] = hexToRgb(hex);
	return (
		0.2126 * channelToLinear(r) +
		0.7152 * channelToLinear(g) +
		0.0722 * channelToLinear(b)
	);
}

function inferModeFromBackground(background: string): ThemeMode {
	return relativeLuminance(background) > 0.42 ? 'light' : 'dark';
}

function normalizeCustomPalette(input: Partial<ThemeCustomPalette> | null | undefined): ThemeCustomPalette {
	return {
		accent: normalizeHexColor(input?.accent, DEFAULT_CUSTOM_THEME.accent),
		background: normalizeHexColor(input?.background, DEFAULT_CUSTOM_THEME.background),
		text: normalizeHexColor(input?.text, DEFAULT_CUSTOM_THEME.text),
	};
}

function getContrastColor(background: string, dark = '#111827', light = '#ffffff'): string {
	return relativeLuminance(background) > 0.52 ? dark : light;
}

function selectionFromState(state: ThemeState): ThemeSelection {
	return {
		preset: state.preset,
		modePreference: state.modePreference,
		custom: state.custom,
	};
}

function buildCustomTokens(custom: ThemeCustomPalette): ThemeTokenMap {
	const palette = normalizeCustomPalette(custom);
	const mode = inferModeFromBackground(palette.background);
	const brightenWith = mode === 'dark' ? '#ffffff' : '#000000';
	const success = mode === 'dark' ? '#86efac' : '#15803d';
	const error = mode === 'dark' ? '#fda4af' : '#be123c';
	const warning = mode === 'dark' ? '#fcd34d' : '#b45309';
	const info = mixHex(palette.accent, brightenWith, mode === 'dark' ? 0.12 : 0.18);

	return {
		'--color-accent': palette.accent,
		'--color-accent-hover': mixHex(palette.accent, brightenWith, mode === 'dark' ? 0.14 : 0.2),
		'--color-accent-contrast': getContrastColor(palette.accent),
		'--color-success': success,
		'--color-success-contrast': getContrastColor(success),
		'--color-error': error,
		'--color-error-contrast': getContrastColor(error),
		'--color-warning': warning,
		'--color-warning-contrast': getContrastColor(warning),
		'--color-info': info,
		'--color-info-contrast': getContrastColor(info),
		'--color-bg-primary': palette.background,
		'--color-bg-secondary': mixHex(palette.background, palette.text, 0.05),
		'--color-bg-tertiary': mixHex(palette.background, palette.text, 0.11),
		'--color-bg-hover': mixHex(palette.background, palette.text, 0.17),
		'--color-text-primary': palette.text,
		'--color-text-secondary': mixHex(palette.text, palette.background, 0.18),
		'--color-text-muted': mixHex(palette.text, palette.background, 0.4),
		'--color-border': mixHex(palette.background, palette.text, 0.18),
		'--color-border-hover': mixHex(palette.background, palette.text, 0.28),
		'--shadow': mode === 'dark'
			? '0 2px 8px rgba(0, 0, 0, 0.32)'
			: '0 2px 8px rgba(15, 23, 42, 0.14)',
		'--color-tunneled-row': hexToRgba(palette.accent, mode === 'dark' ? 0.06 : 0.1),
	};
}

function resolveLegacyMode(selection: ThemeSelection): ThemeMode {
	if (selection.preset === 'custom') {
		return inferModeFromBackground(selection.custom.background);
	}
	if (selection.modePreference === 'system') {
		return getSystemPreferredMode();
	}
	return selection.modePreference;
}

function resolveThemeMode(selection: ThemeSelection): ThemeMode {
	const legacyMode = resolveLegacyMode(selection);
	if (selection.preset === 'custom') {
		return legacyMode;
	}
	/* Neo «светлая» — палитра тёмного Gruvbox; для color-scheme и data-theme оставляем dark */
	if (selection.preset === 'neo' && legacyMode === 'light') {
		return 'dark';
	}
	return legacyMode;
}

function resolveToggledModePreference(selection: ThemeSelection): ThemeMode {
	if (selection.modePreference === 'system') {
		const legacyMode = resolveLegacyMode(selection);
		return legacyMode === 'dark' ? 'light' : 'dark';
	}
	return selection.modePreference === 'dark' ? 'light' : 'dark';
}

export function resolveThemeTokens(selection: ThemeSelection): ThemeTokenMap {
	const legacyMode = resolveLegacyMode(selection);
	if (selection.preset === 'legacy') {
		return legacyMode === 'light' ? LEGACY_LIGHT_TOKENS : LEGACY_DARK_TOKENS;
	}
	if (selection.preset === 'neo') {
		return legacyMode === 'light' ? NEO_LIGHT_TOKENS : NEO_DARK_TOKENS;
	}
	if (selection.preset === 'mint') {
		return legacyMode === 'light' ? MINT_LIGHT_TOKENS : MINT_DARK_TOKENS;
	}
	return buildCustomTokens(selection.custom);
}

export function getThemePreviewStyle(selection: ThemeSelection): string {
	return Object.entries(resolveThemeTokens(selection))
		.map(([name, value]) => `${name}: ${value}`)
		.join('; ');
}

function buildThemeState(selection: ThemeSelection): ThemeState {
	const normalizedSelection: ThemeSelection = {
		preset: selection.preset,
		modePreference: selection.modePreference,
		custom: normalizeCustomPalette(selection.custom),
	};
	const presetMeta = THEME_PRESETS[normalizedSelection.preset];
	return {
		...normalizedSelection,
		legacyMode: resolveLegacyMode(normalizedSelection),
		mode: resolveThemeMode(normalizedSelection),
		label: presetMeta.label,
		summary: presetMeta.summary,
		supportsModeToggle: presetMeta.supportsModeToggle,
	};
}

function persistSelection(selection: ThemeSelection): void {
	localStorage.setItem(storageKey, JSON.stringify(selection));
}

function applyThemeChromeMetadata(tokens: ThemeTokenMap, mode: ThemeMode): void {
	const themeColor =
		tokens['--color-bg-secondary'] ??
		tokens['--color-bg-primary'] ??
		(mode === 'light' ? '#f0f0f3' : '#16161e');

	const themeColorMetas = Array.from(
		document.querySelectorAll<HTMLMetaElement>('meta[name="theme-color"]'),
	);

	if (themeColorMetas.length === 0) {
		const meta = document.createElement('meta');
		meta.setAttribute('name', 'theme-color');
		document.head.appendChild(meta);
		themeColorMetas.push(meta);
	}

	for (const meta of themeColorMetas) {
		meta.setAttribute('content', themeColor);
	}

	let appleStatusMeta = document.querySelector<HTMLMetaElement>(
		'meta[name="apple-mobile-web-app-status-bar-style"]',
	);
	if (!appleStatusMeta) {
		appleStatusMeta = document.createElement('meta');
		appleStatusMeta.setAttribute('name', 'apple-mobile-web-app-status-bar-style');
		document.head.appendChild(appleStatusMeta);
	}
	appleStatusMeta.setAttribute('content', mode === 'light' ? 'default' : 'black');
}

function applyThemeState(state: ThemeState, options: ApplyThemeStateOptions = {}): void {
	const root = document.documentElement;
	const tokens = resolveThemeTokens(selectionFromState(state));

	for (const variableName of THEME_VARIABLE_KEYS) {
		root.style.removeProperty(variableName);
	}
	for (const [variableName, value] of Object.entries(tokens)) {
		root.style.setProperty(variableName, value);
	}

	root.setAttribute('data-theme', state.mode);
	root.setAttribute('data-theme-preset', state.preset);
	root.classList.toggle('light', state.mode === 'light');
	root.style.colorScheme = state.mode;
	applyThemeChromeMetadata(tokens, state.mode);

	if (options.refreshDynamicFavicon) {
		refreshDynamicFavicon(tokens);
	} else {
		applyCachedDynamicFavicon(tokens);
	}
}

function getSystemPreferredMode(): ThemeMode {
	if (!browser) return 'dark';
	return window.matchMedia(SYSTEM_LIGHT_MEDIA_QUERY).matches ? 'light' : 'dark';
}

function getInitialSelection(): ThemeSelection {
	const fallback: ThemeSelection = {
		preset: 'legacy',
		modePreference: 'system',
		custom: DEFAULT_CUSTOM_THEME,
	};
	if (!browser) return fallback;

	const stored = localStorage.getItem(storageKey);
	if (!stored) return fallback;

	if (isThemeMode(stored)) {
		return { ...fallback, preset: 'legacy', modePreference: stored };
	}

	try {
		const parsed = JSON.parse(stored) as
			| (Partial<ThemeSelection> & { legacyMode?: string })
			| null;
		return {
			preset: isThemePreset(parsed?.preset) ? parsed.preset : fallback.preset,
			modePreference: isThemeModePreference(parsed?.modePreference)
				? parsed.modePreference
				: isThemeMode(parsed?.legacyMode)
					? parsed.legacyMode
					: fallback.modePreference,
			custom: normalizeCustomPalette(parsed?.custom),
		};
	} catch {
		return fallback;
	}
}

function createThemeStore() {
	let currentState = buildThemeState(getInitialSelection());
	const { subscribe, set } = writable<ThemeState>(currentState);
	let mediaQueryList: MediaQueryList | null = null;

	function commit(
		selection: ThemeSelection,
		options: { refreshDynamicFavicon?: boolean } = {},
	): ThemeState {
		const previousAccent = getStateAccent(currentState);
		const nextState = buildThemeState(selection);
		const nextAccent = getStateAccent(nextState);
		const accentChanged = previousAccent !== nextAccent;

		if (browser) {
			persistSelection(selectionFromState(nextState));
			applyThemeState(nextState, {
				refreshDynamicFavicon: (options.refreshDynamicFavicon ?? true) && accentChanged,
			});
		}
		currentState = nextState;
		set(nextState);
		return nextState;
	}

	function mutate(transform: (selection: ThemeSelection) => ThemeSelection): ThemeState {
		return commit(transform(selectionFromState(currentState)));
	}

	function refreshFromSystemPreference(): void {
		if (!browser) return;
		if (currentState.preset === 'custom' || currentState.modePreference !== 'system') return;
		commit(selectionFromState(currentState));
	}

	function startSystemPreferenceSync(): void {
		if (!browser || mediaQueryList) return;
		mediaQueryList = window.matchMedia(SYSTEM_LIGHT_MEDIA_QUERY);
		const listener = () => refreshFromSystemPreference();
		if (typeof mediaQueryList.addEventListener === 'function') {
			mediaQueryList.addEventListener('change', listener);
			return;
		}
		mediaQueryList.addListener(listener);
	}

	return {
		subscribe,
		init: () => {
			startSystemPreferenceSync();
			commit(getInitialSelection(), { refreshDynamicFavicon: false });
		},
		cyclePreset: () => {
			mutate((current) => {
				const currentIndex = presetCycleOrder.indexOf(current.preset);
				const nextPreset = presetCycleOrder[(currentIndex + 1) % presetCycleOrder.length];
				return { ...current, preset: nextPreset };
			});
		},
		setPreset: (preset: ThemePreset) => {
			mutate((current) => ({ ...current, preset }));
		},
		setMode: (mode: ThemeModePreference) => {
			mutate((current) => ({ ...current, modePreference: mode }));
		},
		toggleMode: () => {
			mutate((current) => {
				if (current.preset === 'custom') return current;
				return {
					...current,
					modePreference: resolveToggledModePreference(current),
				};
			});
		},
		updateCustom: (patch: Partial<ThemeCustomPalette>) => {
			mutate((current) => ({
				...current,
				preset: 'custom',
				custom: normalizeCustomPalette({ ...current.custom, ...patch }),
			}));
		},
		resetCustom: () => {
			mutate((current) => ({
				...current,
				custom: DEFAULT_CUSTOM_THEME,
			}));
		},
	};
}

export const theme = createThemeStore();
