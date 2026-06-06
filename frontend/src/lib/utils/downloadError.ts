/**
 * Centralized humanization of "service download" errors — geo.dat, AWGM
 * self-update / update check, DNSRoute URL-lists, Amnezia Premium, the
 * download-route list, etc. All of these run through the configurable
 * download route (Direct / AWG-bind / sing-box / subscription), so they
 * share the same failure modes.
 *
 * The backend emits stable `DOWNLOAD_*` codes via RouteError. This module
 * maps codes to friendly UI text and keeps string fallbacks only for legacy
 * cached errors without a code.
 */

export type DownloadErrorKind =
	| 'singbox-off'
	| 'singbox-route'
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
	/** Backend envelope code, when present (e.g. DOWNLOAD_SINGBOX_EGRESS_FAILED). */
	code?: string;
}

/** Deep-link that scrolls to and glows the "Загрузки и обновления" card. */
export const DOWNLOAD_SETTINGS_HREF = '/settings?highlight=downloads#downloads';

/** Stable download error codes emitted by internal/downloader. */
export const DOWNLOAD_ERROR_CODES = {
	SINGBOX_NOT_RUNNING: 'DOWNLOAD_SINGBOX_NOT_RUNNING',
	SINGBOX_NOT_READY: 'DOWNLOAD_SINGBOX_NOT_READY',
	SINGBOX_EGRESS_FAILED: 'DOWNLOAD_SINGBOX_EGRESS_FAILED',
	AWG_DOWN: 'DOWNLOAD_AWG_DOWN',
	TIMEOUT: 'DOWNLOAD_TIMEOUT',
	NETWORK: 'DOWNLOAD_NETWORK',
	ROUTE: 'DOWNLOAD_ROUTE',
} as const;

interface ExtractedError {
	raw: string;
	code: string;
}

/** Build input for humanizeDownloadError from message + optional backend code. */
export function downloadRouteError(message?: string, code?: string): unknown {
	if (!message) return null;
	if (code) return { message, code };
	return message;
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
			error?: string;
			errorCode?: string;
			body?: { code?: string; message?: string };
		};
		const code = e.body?.code || e.code || e.errorCode || '';
		const raw = e.body?.message || e.message || e.error || String(err);
		return { raw, code };
	}
	return { raw: String(err), code: '' };
}

function kindFromCode(code: string): DownloadErrorKind | null {
	switch (code) {
		case DOWNLOAD_ERROR_CODES.SINGBOX_NOT_RUNNING:
		case DOWNLOAD_ERROR_CODES.SINGBOX_NOT_READY:
			return 'singbox-off';
		case DOWNLOAD_ERROR_CODES.SINGBOX_EGRESS_FAILED:
			return 'singbox-route';
		case DOWNLOAD_ERROR_CODES.AWG_DOWN:
			return 'awg-down';
		case DOWNLOAD_ERROR_CODES.TIMEOUT:
			return 'timeout';
		case DOWNLOAD_ERROR_CODES.NETWORK:
			return 'network';
		case DOWNLOAD_ERROR_CODES.ROUTE:
			return 'route';
		default:
			return null;
	}
}

function classify(raw: string, code: string): DownloadErrorKind {
	const fromCode = kindFromCode(code);
	if (fromCode) return fromCode;

	// Legacy fallback for cached/plain-string errors without a backend code.
	const text = `${raw} ${code}`.toLowerCase();

	if (
		text.includes('sing-box is not running') ||
		text.includes('sing-box is not ready') ||
		text.includes('sing-box proxy port') ||
		text.includes('subscription proxy port') ||
		text.includes('proxy port not found') ||
		text.includes('proxy port not listening')
	) {
		return 'singbox-off';
	}

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
		text.includes('download via') &&
		(text.includes('(singbox)') || text.includes('(subscription)'))
	) {
		if (
			text.includes('connection refused') ||
			text.includes('proxy port not listening') ||
			text.includes('sing-box is not ready')
		) {
			return 'singbox-off';
		}
		if (
			text.includes('eof') ||
			text.includes('connection reset') ||
			text.includes('connection broken') ||
			text.includes('broken pipe') ||
			text.includes('malformed http response') ||
			text.includes('ошибка сети')
		) {
			return 'singbox-route';
		}
	}

	if (
		text.includes('eof') ||
		text.includes('connection reset') ||
		text.includes('connection broken') ||
		text.includes('connection refused') ||
		text.includes('broken pipe') ||
		text.includes('malformed http response') ||
		text.includes('ошибка сети')
	) {
		return 'network';
	}

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
 */
export function humanizeDownloadError(err: unknown): HumanizedDownloadError {
	const { raw, code } = extractError(err);
	const kind = classify(raw, code);

	switch (kind) {
		case 'singbox-off': {
			const starting =
				code === DOWNLOAD_ERROR_CODES.SINGBOX_NOT_READY ||
				raw.toLowerCase().includes('not ready') ||
				raw.toLowerCase().includes('proxy port not listening') ||
				(raw.toLowerCase().includes('connection refused') &&
					raw.toLowerCase().includes('download via') &&
					(raw.toLowerCase().includes('(singbox)') ||
						raw.toLowerCase().includes('(subscription)')));
			return {
				kind,
				code,
				raw,
				needsDownloadSettings: true,
				title: starting
					? 'Sing-box ещё запускается.'
					: 'Маршрут загрузки требует запущенный sing-box.',
				detail: starting
					? 'Локальный прокси sing-box пока недоступен. Подождите несколько секунд и повторите либо выберите другой маршрут.'
					: 'Включите sing-box или выберите другой маршрут (Direct или AWG-туннель).',
			};
		}
		case 'singbox-route':
			return {
				kind,
				code,
				raw,
				needsDownloadSettings: true,
				title: 'Не удалось загрузить через sing-box-маршрут.',
				detail:
					'Туннель маршрута не смог установить соединение с сервером. Проверьте его состояние, перезапустите sing-box или выберите другой маршрут загрузок.',
			};
		case 'awg-down':
			return {
				kind,
				code,
				raw,
				needsDownloadSettings: true,
				title: 'AWG-туннель маршрута загрузки недоступен.',
				detail:
					'Туннель выключен или его интерфейс не поднят. Запустите туннель или выберите другой маршрут.',
			};
		case 'timeout':
			return {
				kind,
				code,
				raw,
				needsDownloadSettings: true,
				title: 'Превышено время ожидания загрузки.',
				detail:
					'Сервер не ответил вовремя. Проверьте соединение/маршрут загрузок и попробуйте ещё раз.',
			};
		case 'network':
			return {
				kind,
				code,
				raw,
				needsDownloadSettings: true,
				title: 'Не удалось установить соединение.',
				detail:
					'Соединение оборвалось — возможно, маршрут блокируется или нестабилен. Попробуйте другой маршрут загрузок.',
			};
		case 'route':
			return {
				kind,
				code,
				raw,
				needsDownloadSettings: true,
				title: 'Маршрут загрузки недоступен.',
				detail: 'Проверьте маршрут служебных загрузок и попробуйте снова.',
			};
		default:
			return {
				kind,
				code,
				raw,
				needsDownloadSettings: false,
				title: raw || 'Не удалось выполнить загрузку.',
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
	else if (h.needsDownloadSettings) parts.push('Откройте Настройки → Загрузки.');
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
