import { m } from '$lib/i18n';
import { iconImageSrc } from '$lib/utils/icon-url-meta';

/** Max size for inline / uploaded custom icons (base64 data URLs). */
export const MAX_CUSTOM_ICON_BYTES = 96 * 1024;

const UNSAFE_SVG_RE =
	/<script\b|on\w+\s*=|javascript:|data:text\/html|<foreignObject\b/i;

export function isDataIconUrl(url: string | undefined): boolean {
	return typeof url === 'string' && url.startsWith('data:image/');
}

/** Short label for edit forms (hides long data URLs and tile-bg fragment). */
export function formatIconUrlHint(url: string): string {
	const src = iconImageSrc(url);
	const hasTileBg = url !== src;
	if (isDataIconUrl(src)) {
		const kind = src.startsWith('data:image/svg')
			? m.custom_icon_kind_svg()
			: m.custom_icon_kind_image();
		return hasTileBg ? m.custom_icon_with_own_bg({ label: kind }) : kind;
	}
	const hint = src.length > 72 ? `${src.slice(0, 69)}…` : src;
	return hasTileBg ? m.custom_icon_with_own_bg({ label: hint }) : hint;
}

function estimateDataUrlBytes(dataUrl: string): number {
	const comma = dataUrl.indexOf(',');
	if (comma < 0) return dataUrl.length;
	const payload = dataUrl.slice(comma + 1);
	// base64 ≈ 3/4 of raw bytes; URI-encoded SVG is smaller but we stay conservative
	if (dataUrl.slice(0, comma).includes('base64')) {
		return Math.ceil((payload.length * 3) / 4);
	}
	return decodeURIComponent(payload).length;
}

export function assertIconSizeOk(dataUrl: string): void {
	const bytes = estimateDataUrlBytes(dataUrl);
	if (bytes > MAX_CUSTOM_ICON_BYTES) {
		throw new Error(
			m.custom_icon_too_big({
				size: Math.round(bytes / 1024),
				max: Math.round(MAX_CUSTOM_ICON_BYTES / 1024),
			}),
		);
	}
}

/** Extract inner SVG from a fragment or full document; returns null if invalid/unsafe. */
export function parseSvgMarkup(input: string): string | null {
	const trimmed = input.trim();
	if (!trimmed) return null;
	if (UNSAFE_SVG_RE.test(trimmed)) return null;

	const doc = new DOMParser().parseFromString(trimmed, 'image/svg+xml');
	const root = doc.documentElement;
	if (root.tagName.toLowerCase() !== 'svg') return null;
	if (doc.querySelector('parsererror')) return null;
	if (UNSAFE_SVG_RE.test(root.outerHTML)) return null;

	return root.outerHTML;
}

export function svgMarkupToDataUrl(svg: string): string {
	const parsed = parseSvgMarkup(svg);
	if (!parsed) throw new Error(m.custom_icon_bad_svg());
	const encoded = encodeURIComponent(parsed)
		.replace(/'/g, '%27')
		.replace(/"/g, '%22');
	const dataUrl = `data:image/svg+xml;charset=utf-8,${encoded}`;
	assertIconSizeOk(dataUrl);
	return dataUrl;
}

export function dataUrlToSvgMarkup(dataUrl: string): string | null {
	if (!dataUrl.startsWith('data:image/svg+xml')) return null;
	const comma = dataUrl.indexOf(',');
	if (comma < 0) return null;
	const meta = dataUrl.slice(0, comma);
	const payload = dataUrl.slice(comma + 1);
	try {
		const raw = meta.includes('base64')
			? atob(payload)
			: decodeURIComponent(payload);
		return parseSvgMarkup(raw);
	} catch {
		return null;
	}
}

const ACCEPTED_IMAGE_TYPES = new Set([
	'image/png',
	'image/jpeg',
	'image/webp',
	'image/gif',
	'image/svg+xml',
]);

export function isAcceptedIconFile(file: File): boolean {
	if (ACCEPTED_IMAGE_TYPES.has(file.type)) return true;
	const ext = file.name.split('.').pop()?.toLowerCase();
	return ext === 'svg' || ext === 'png' || ext === 'jpg' || ext === 'jpeg' || ext === 'webp';
}

export async function fileToIconDataUrl(file: File): Promise<string> {
	if (!isAcceptedIconFile(file)) {
		throw new Error(m.custom_icon_unsupported_file());
	}
	if (file.size > MAX_CUSTOM_ICON_BYTES) {
		throw new Error(
			m.custom_icon_file_too_big({
				size: Math.round(file.size / 1024),
				max: Math.round(MAX_CUSTOM_ICON_BYTES / 1024),
			}),
		);
	}

	if (file.type === 'image/svg+xml' || file.name.toLowerCase().endsWith('.svg')) {
		const text = await file.text();
		return svgMarkupToDataUrl(text);
	}

	const dataUrl = await new Promise<string>((resolve, reject) => {
		const reader = new FileReader();
		reader.onload = () => resolve(String(reader.result));
		reader.onerror = () => reject(new Error(m.custom_icon_read_failed()));
		reader.readAsDataURL(file);
	});

	assertIconSizeOk(dataUrl);
	return dataUrl;
}
