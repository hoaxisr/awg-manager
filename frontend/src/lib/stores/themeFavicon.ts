// Динамический favicon: перекрашивание /favicon.svg в акцент активной
// темы + кэш в localStorage. Выделено из theme.ts — тема вызывает
// refreshDynamicFavicon/applyCachedDynamicFavicon при применении состояния.
import { browser } from '$app/environment';
import type { ThemeTokenMap } from './theme';
// Функции-декларации из theme.ts (hoisted) — цикл модулей безопасен:
// на этапе инициализации favicon-модуль их не вызывает.
import { DEFAULT_CUSTOM_THEME, normalizeHexColor } from './theme';

const faviconStorageKey = 'awg-manager-dynamic-favicon';

const faviconCacheVersion = 1;

const faviconTemplateUrl = '/favicon.svg';

const dynamicFaviconSelector = 'link[data-awgm-dynamic-favicon]';

const staticFaviconSelector = 'link[data-awgm-static-favicon]';

const faviconAccentPattern = /#e8a33d/gi;

interface CachedDynamicFavicon {
	version: number;
	accent: string;
	href: string;
}

let faviconSvgTemplatePromise: Promise<string> | null = null;

let dynamicFaviconUpdateSeq = 0;

export function getFaviconAccent(tokens: ThemeTokenMap): string {
	return normalizeHexColor(tokens['--color-accent'], DEFAULT_CUSTOM_THEME.accent);
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

export function applyCachedDynamicFavicon(tokens: ThemeTokenMap): void {
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

export function refreshDynamicFavicon(tokens: ThemeTokenMap): void {
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
