import { browser } from '$app/environment';
import { writable } from 'svelte/store';
import {
	applyCachedDynamicFavicon,
	getFaviconAccent,
	refreshDynamicFavicon
} from './themeFavicon';

export type ThemePreset = 'grafit' | 'sever' | 'mokh' | 'mint' | 'custom';
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
	/** После удаления пресета neo тождественно `mode`; кандидат на чистку. */
	legacyMode: ThemeMode;
	mode: ThemeMode;
	label: string;
	summary: string;
	supportsModeToggle: boolean;
}

export type ThemeTokenMap = Record<string, string>;

const storageKey = 'awg-manager-theme';
const presetCycleOrder: ThemePreset[] = [
	'grafit',
	'sever',
	'mokh',
	'mint',
	'custom',
];
const SYSTEM_LIGHT_MEDIA_QUERY = '(prefers-color-scheme: light)';



interface ApplyThemeStateOptions {
	refreshDynamicFavicon?: boolean;
}


export const DEFAULT_CUSTOM_THEME: ThemeCustomPalette = {
	accent: '#8b5cf6',
	background: '#111827',
	text: '#f8fafc',
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

/** Графит — нейтральный графит с янтарным акцентом */
const GRAFIT_DARK_TOKENS: ThemeTokenMap = {
	'--color-accent': '#e8a33d',
	'--color-accent-hover': '#f2b657',
	'--color-accent-contrast': '#131315',
	'--color-success': '#6fb388',
	'--color-success-contrast': '#131315',
	'--color-error': '#d47585',
	'--color-error-contrast': '#131315',
	'--color-warning': '#d9a05b',
	'--color-warning-contrast': '#131315',
	'--color-info': '#7aa6c2',
	'--color-info-contrast': '#131315',
	'--color-bg-primary': '#131315',
	'--color-bg-secondary': '#1b1b1f',
	'--color-bg-tertiary': '#26262c',
	'--color-bg-hover': '#303038',
	'--color-text-primary': '#e8e8ec',
	'--color-text-secondary': '#b6b6c2',
	'--color-text-muted': '#74747f',
	'--color-border': '#34343d',
	'--color-border-hover': '#585860',
	'--shadow': '0 2px 8px rgba(0, 0, 0, 0.3)',
	'--color-tunneled-row': 'rgba(232, 163, 61, 0.03)',
};

const GRAFIT_LIGHT_TOKENS: ThemeTokenMap = {
	'--color-accent': '#b97a1e',
	'--color-accent-hover': '#a06715',
	'--color-accent-contrast': '#131315',
	'--color-success': '#3d7d55',
	'--color-success-contrast': '#ffffff',
	'--color-error': '#a94b5c',
	'--color-error-contrast': '#ffffff',
	'--color-warning': '#9a742e',
	'--color-warning-contrast': '#131315',
	'--color-info': '#3f6f8e',
	'--color-info-contrast': '#ffffff',
	'--color-bg-primary': '#ececee',
	'--color-bg-secondary': '#f7f7f8',
	'--color-bg-tertiary': '#dfdfe3',
	'--color-bg-hover': '#d2d2d8',
	'--color-text-primary': '#1d1d24',
	'--color-text-secondary': '#41414c',
	'--color-text-muted': '#6d6d78',
	'--color-border': '#c2c2c9',
	'--color-border-hover': '#a1a1a8',
	'--shadow': '0 2px 10px rgba(29, 29, 36, 0.08)',
	'--color-tunneled-row': 'rgba(185, 122, 30, 0.07)',
};

/** Север — холодная сине-серая гамма */
const SEVER_DARK_TOKENS: ThemeTokenMap = {
	'--color-accent': '#7fb3c8',
	'--color-accent-hover': '#93c4d8',
	'--color-accent-contrast': '#14181f',
	'--color-success': '#8fb573',
	'--color-success-contrast': '#14181f',
	'--color-error': '#c76b74',
	'--color-error-contrast': '#14181f',
	'--color-warning': '#d1a15e',
	'--color-warning-contrast': '#14181f',
	'--color-info': '#81a1c1',
	'--color-info-contrast': '#14181f',
	'--color-bg-primary': '#14181f',
	'--color-bg-secondary': '#1c222b',
	'--color-bg-tertiary': '#272f3a',
	'--color-bg-hover': '#333d4b',
	'--color-text-primary': '#dde3ec',
	'--color-text-secondary': '#a9b4c4',
	'--color-text-muted': '#6e7a8c',
	'--color-border': '#39434f',
	'--color-border-hover': '#5a636e',
	'--shadow': '0 2px 8px rgba(0, 0, 0, 0.3)',
	'--color-tunneled-row': 'rgba(127, 179, 200, 0.03)',
};

const SEVER_LIGHT_TOKENS: ThemeTokenMap = {
	'--color-accent': '#3a7d99',
	'--color-accent-hover': '#2f6a83',
	'--color-accent-contrast': '#ffffff',
	'--color-success': '#4c7d3a',
	'--color-success-contrast': '#ffffff',
	'--color-error': '#a44e58',
	'--color-error-contrast': '#ffffff',
	'--color-warning': '#96703a',
	'--color-warning-contrast': '#ffffff',
	'--color-info': '#46698f',
	'--color-info-contrast': '#ffffff',
	'--color-bg-primary': '#e8ebef',
	'--color-bg-secondary': '#f4f6f8',
	'--color-bg-tertiary': '#d9dee5',
	'--color-bg-hover': '#cbd2db',
	'--color-text-primary': '#242b36',
	'--color-text-secondary': '#46505e',
	'--color-text-muted': '#6b7684',
	'--color-border': '#b9c1cb',
	'--color-border-hover': '#9ba3ad',
	'--shadow': '0 2px 10px rgba(36, 43, 54, 0.08)',
	'--color-tunneled-row': 'rgba(58, 125, 153, 0.07)',
};

/** Мох — тёплый гравий с мшистым акцентом */
const MOKH_DARK_TOKENS: ThemeTokenMap = {
	'--color-accent': '#97a97c',
	'--color-accent-hover': '#a9bb8e',
	'--color-accent-contrast': '#171614',
	'--color-success': '#6faf7f',
	'--color-success-contrast': '#171614',
	'--color-error': '#c96f66',
	'--color-error-contrast': '#171614',
	'--color-warning': '#cc9a4e',
	'--color-warning-contrast': '#171614',
	'--color-info': '#7fa6a3',
	'--color-info-contrast': '#171614',
	'--color-bg-primary': '#171614',
	'--color-bg-secondary': '#201e1b',
	'--color-bg-tertiary': '#2b2925',
	'--color-bg-hover': '#383530',
	'--color-text-primary': '#e6e1d7',
	'--color-text-secondary': '#bdb6a8',
	'--color-text-muted': '#7d776b',
	'--color-border': '#3b3833',
	'--color-border-hover': '#5d5a54',
	'--shadow': '0 2px 8px rgba(0, 0, 0, 0.3)',
	'--color-tunneled-row': 'rgba(151, 169, 124, 0.03)',
};

const MOKH_LIGHT_TOKENS: ThemeTokenMap = {
	'--color-accent': '#5f7440',
	'--color-accent-hover': '#4e6134',
	'--color-accent-contrast': '#ffffff',
	'--color-success': '#3f7d50',
	'--color-success-contrast': '#ffffff',
	'--color-error': '#a1493f',
	'--color-error-contrast': '#ffffff',
	'--color-warning': '#8f6a2a',
	'--color-warning-contrast': '#ffffff',
	'--color-info': '#3f6f6b',
	'--color-info-contrast': '#ffffff',
	'--color-bg-primary': '#eceae5',
	'--color-bg-secondary': '#f6f5f1',
	'--color-bg-tertiary': '#dedbd3',
	'--color-bg-hover': '#d0ccc2',
	'--color-text-primary': '#26241f',
	'--color-text-secondary': '#4a463e',
	'--color-text-muted': '#6f6a5f',
	'--color-border': '#c4bfb3',
	'--color-border-hover': '#a4a095',
	'--shadow': '0 2px 10px rgba(38, 36, 31, 0.08)',
	'--color-tunneled-row': 'rgba(95, 116, 64, 0.07)',
};

export const THEME_PRESETS = {
	grafit: {
		label: 'AWGM - Графит',
		summary: 'Нейтральный графитовый фон и тёплый янтарный акцент.',
		supportsModeToggle: true,
	},
	sever: {
		label: 'AWGM - Север',
		summary: 'Холодная сине-серая гамма со спокойным ледяным акцентом.',
		supportsModeToggle: true,
	},
	mokh: {
		label: 'AWGM - Мох',
		summary: 'Тёплые гравийные тона и приглушённый мшисто-зелёный акцент.',
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
		...Object.keys(MINT_DARK_TOKENS),
		...Object.keys(MINT_LIGHT_TOKENS),
		...Object.keys(GRAFIT_DARK_TOKENS),
		...Object.keys(GRAFIT_LIGHT_TOKENS),
		...Object.keys(SEVER_DARK_TOKENS),
		...Object.keys(SEVER_LIGHT_TOKENS),
		...Object.keys(MOKH_DARK_TOKENS),
		...Object.keys(MOKH_LIGHT_TOKENS),
	]),
];

function isThemeMode(value: string | null | undefined): value is ThemeMode {
	return value === 'dark' || value === 'light';
}

function isThemeModePreference(value: string | null | undefined): value is ThemeModePreference {
	return value === 'system' || isThemeMode(value);
}

function isThemePreset(value: string | null | undefined): value is ThemePreset {
	return (
		value === 'grafit' ||
		value === 'sever' ||
		value === 'mokh' ||
		value === 'mint' ||
		value === 'custom'
	);
}

export function normalizeHexColor(value: string | null | undefined, fallback: string): string {
	if (!value) return fallback;
	const match = /^#([0-9a-f]{6})$/i.exec(value.trim());
	return match ? `#${match[1].toLowerCase()}` : fallback;
}


function getStateAccent(state: ThemeState): string {
	return getFaviconAccent(resolveThemeTokens(selectionFromState(state)));
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

/** После удаления пресета neo расхождений с resolveLegacyMode не осталось. */
function resolveThemeMode(selection: ThemeSelection): ThemeMode {
	return resolveLegacyMode(selection);
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
	if (selection.preset === 'grafit') {
		return legacyMode === 'light' ? GRAFIT_LIGHT_TOKENS : GRAFIT_DARK_TOKENS;
	}
	if (selection.preset === 'sever') {
		return legacyMode === 'light' ? SEVER_LIGHT_TOKENS : SEVER_DARK_TOKENS;
	}
	if (selection.preset === 'mokh') {
		return legacyMode === 'light' ? MOKH_LIGHT_TOKENS : MOKH_DARK_TOKENS;
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
		(mode === 'light' ? '#f7f7f8' : '#1b1b1f');

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
		preset: 'grafit',
		modePreference: 'system',
		custom: DEFAULT_CUSTOM_THEME,
	};
	if (!browser) return fallback;

	const stored = localStorage.getItem(storageKey);
	if (!stored) return fallback;

	if (isThemeMode(stored)) {
		return { ...fallback, modePreference: stored };
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
