import { QURE_CDN_BASE } from '$lib/generated/qureIcons';
import { iconImageSrc, parseIconUrl } from '$lib/utils/icon-url-meta';

/** Inner Qure PNG size inside the tile (PresetIcon brand art ≈ 0.56). */
export const QURE_ICON_INNER_SCALE = 0.72;

/**
 * Default tile behind Qure / custom URL icons.
 * Uses tertiary surface — lighter than route cards (--bg-secondary) in dark theme
 * and slightly darker in light theme, so icons do not blend into the card.
 */
export const DEFAULT_ICON_TILE_BG =
	'var(--color-icon-tile-bg, var(--color-bg-tertiary, var(--bg-tertiary, #26262c)))';

export function isQureIconUrl(url: string | undefined): url is string {
	return typeof url === 'string' && iconImageSrc(url).startsWith(QURE_CDN_BASE);
}

export function qureIconNameFromUrl(url: string): string | null {
	const src = iconImageSrc(url);
	if (!src.startsWith(QURE_CDN_BASE)) return null;
	try {
		const path = src.slice(QURE_CDN_BASE.length);
		const match = path.match(/^\/(.+)\.png$/i);
		return match ? decodeURIComponent(match[1]) : null;
	} catch {
		return null;
	}
}

/** Tile background: user #awg-tile= override, else unified default. */
export function resolveIconTileBackground(_ruleName: string, iconUrl?: string): string {
	if (iconUrl) {
		const { userTileBg } = parseIconUrl(iconUrl);
		if (userTileBg) return userTileBg;
	}
	return DEFAULT_ICON_TILE_BG;
}

/** Read resolved --color-icon-tile-bg from the active theme (for color inputs). */
export function readThemeIconTileHex(): string {
	if (typeof document === 'undefined') return '#26262c';
	const probe = document.createElement('div');
	probe.style.cssText = `position:absolute;visibility:hidden;background:${DEFAULT_ICON_TILE_BG}`;
	document.body.appendChild(probe);
	const hex = rgbCssToHex(getComputedStyle(probe).backgroundColor);
	probe.remove();
	return hex ?? '#26262c';
}

function rgbCssToHex(css: string): string | null {
	const m = css.match(/^rgba?\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)/);
	if (!m) return null;
	return (
		'#' +
		[m[1], m[2], m[3]].map((n) => Number(n).toString(16).padStart(2, '0')).join('')
	);
}
