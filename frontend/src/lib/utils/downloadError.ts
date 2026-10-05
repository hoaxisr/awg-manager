/**
 * Centralized humanization of "service download" errors — geo.dat, AWGM
 * self-update / update check, DNSRoute URL-lists, Amnezia Premium, the
 * download-route list, etc. All of these run through the configurable
 * download route (Direct / AWG-bind / sing-box / subscription), so they
 * share the same failure modes.
 *
 * The backend emits a heterogeneous mix of RU/EN strings and an envelope
 * `code`. Instead of every call site re-implementing regexes, this module
 * classifies the error once and returns a friendly user-facing message plus
 * the raw text (kept for logs / "details"). See humanizeDownloadError().
 */
import { m } from '$lib/i18n';

export type DownloadErrorKind =
	| 'singbox-off'
	| 'awg-down'
	| 'timeout'
	| 'network'
	| 'route'
	| 'generic';

export interface HumanizedDownloadError {
	/** Short, friendly headline shown to the user. */
	title: string;
	/** Optional actionable hint (what to do next). */
	detail?: string;
	kind: DownloadErrorKind;
	/** True when pointing the user at Settings → Downloads is useful. */
	needsDownloadSettings: boolean;
	/** Original backend message — keep for logs / collapsible details. */
	raw: string;
	/** Backend envelope code, when present (e.g. GEO_DOWNLOAD_ROUTE_ERROR). */
	code?: string;
}

/** Deep-link that scrolls to and glows the "Загрузки и обновления" card. */
export const DOWNLOAD_SETTINGS_HREF = '/settings?highlight=downloads#downloads';

interface ExtractedError {
	raw: string;
	code: string;
}

/**
 * Pull a message + envelope code out of whatever a call site caught: an API
 * client Error (with `.body.code` / `.body.message`), a plain Error, a bare
 * string (e.g. `UpdateInfo.error`, `subscription.lastError`), or null.
 */
function extractError(err: unknown): ExtractedError {
	if (err == null) return { raw: '', code: '' };
	if (typeof err === 'string') return { raw: err, code: '' };
	if (typeof err === 'object') {
		const e = err as {
			message?: string;
			code?: string;
			body?: { code?: string; message?: string };
		};
		const code = e.body?.code || e.code || '';
		const raw = e.body?.message || e.message || String(err);
		return { raw, code };
	}
	return { raw: String(err), code: '' };
}

function classify(raw: string, code: string): DownloadErrorKind {
	const text = `${raw} ${code}`.toLowerCase();

	// sing-box-backed route (sing-box tunnel or subscription) but the daemon
	// is down / its local proxy port is not yet listening.
	if (
		text.includes('sing-box is not running') ||
		text.includes('sing-box proxy port') ||
		text.includes('proxy port not found')
	) {
		return 'singbox-off';
	}

	// AWG bind route but the kernel interface is missing / link is down.
	if (
		text.includes('is not present') ||
		text.includes('no such device') ||
		text.includes('network is unreachable') ||
		text.includes('no route to host')
	) {
		return 'awg-down';
	}

	if (
		text.includes('timed out') ||
		text.includes('timeout') ||
		text.includes('deadline exceeded')
	) {
		return 'timeout';
	}

	if (
		text.includes('eof') ||
		text.includes('connection reset') ||
		text.includes('connection broken') ||
		text.includes('connection refused') ||
		text.includes('broken pipe') ||
		text.includes('malformed http response') ||
		code === 'NETWORK_ERROR' // ApiNetworkError клиента: по коду, текст переведён
	) {
		return 'network';
	}

	// Generic "route is unavailable / ambiguous" without a more specific cause.
	if (
		text.includes('is unavailable') ||
		text.includes('unavailable for download transport') ||
		text.includes('is ambiguous') ||
		code.endsWith('ROUTE_ERROR')
	) {
		return 'route';
	}

	return 'generic';
}

/**
 * Classify a caught download error into a friendly, actionable message.
 *
 * Usage:
 *   const h = humanizeDownloadError(e);
 *   notifications.error(downloadErrorToText(h));   // toast (no link)
 *   <DownloadErrorNotice error={e} />              // inline (with link)
 */
export function humanizeDownloadError(err: unknown): HumanizedDownloadError {
	const { raw, code } = extractError(err);
	const kind = classify(raw, code);

	switch (kind) {
		case 'singbox-off':
			return {
				kind,
				code,
				raw,
				needsDownloadSettings: true,
				title: m.download_error_singbox_off_title(),
				detail: m.download_error_singbox_off_detail(),
			};
		case 'awg-down':
			return {
				kind,
				code,
				raw,
				needsDownloadSettings: true,
				title: m.download_error_awg_down_title(),
				detail: m.download_error_awg_down_detail(),
			};
		case 'timeout':
			return {
				kind,
				code,
				raw,
				needsDownloadSettings: true,
				title: m.download_error_timeout_title(),
				detail: m.download_error_timeout_detail(),
			};
		case 'network':
			return {
				kind,
				code,
				raw,
				needsDownloadSettings: true,
				title: m.download_error_network_title(),
				detail: m.download_error_network_detail(),
			};
		case 'route':
			return {
				kind,
				code,
				raw,
				needsDownloadSettings: true,
				title: m.download_error_route_title(),
				detail: m.download_error_route_detail(),
			};
		default:
			return {
				kind,
				code,
				raw,
				needsDownloadSettings: false,
				title: raw || m.download_error_generic_title(),
			};
	}
}

/**
 * Flatten a humanized error into a single line for toast contexts that can't
 * render a link. Appends a "go to settings" cue when relevant.
 */
export function downloadErrorToText(input: unknown): string {
	const h =
		isHumanized(input) ? input : humanizeDownloadError(input);
	const parts = [h.title];
	if (h.detail) parts.push(h.detail);
	else if (h.needsDownloadSettings) parts.push(m.download_error_open_settings());
	return parts.join(' ');
}

function isHumanized(v: unknown): v is HumanizedDownloadError {
	return (
		typeof v === 'object' &&
		v !== null &&
		'kind' in v &&
		'needsDownloadSettings' in v
	);
}
