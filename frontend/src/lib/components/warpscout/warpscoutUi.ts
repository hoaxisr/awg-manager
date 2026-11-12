// Вспомогательные функции вкладки «WARP Сканер»: флаги, подписи
// состояний автопилота, разбор ошибок API и форматирование времени.
import type { BadgeVariant } from '$lib/components/ui';
import { formatLocale, m } from '$lib/i18n';
import type {
	WarpConfig,
	WarpFilters,
	WarpProfile,
	WarpTargetMode,
	WarpTunnelRef,
} from '$lib/types/warpscout';

export const QUICK_COLOS = ['LED', 'KJA', 'OVB', 'SVX'];
export const SIGNATURE_PROFILES = ['quic_initial', 'dns', 'stun', 'dtls', 'sip', 'none'] as const;

export function modeLabel(mode: WarpTargetMode): string {
	switch (mode) {
		case 'kernel':
			return 'Kernel AWG';
		case 'native':
			return 'Native WG';
		case 'system':
			return m.warpscout_mode_system();
		default:
			return mode;
	}
}

/** Эмодзи-флаг по ISO 3166-1 alpha-2 (пусто для нестандартных кодов). */
export function countryFlag(code: string | undefined | null): string {
	const cc = (code ?? '').trim().toUpperCase();
	if (!/^[A-Z]{2}$/.test(cc)) return '';
	const base = 0x1f1e6 - 'A'.charCodeAt(0);
	return String.fromCodePoint(base + cc.charCodeAt(0), base + cc.charCodeAt(1));
}

export interface WatchStateView {
	label: string;
	variant: BadgeVariant;
	tone: 'ok' | 'warn' | 'err' | 'info' | 'muted';
}

export function watchStateView(state: string | undefined): WatchStateView {
	if (!state) return { label: m.warpscout_state_no_data(), variant: 'muted', tone: 'muted' };
	switch (state) {
		case 'idle':
			return { label: m.warpscout_state_idle(), variant: 'muted', tone: 'muted' };
		case 'ok':
			return { label: m.warpscout_state_ok(), variant: 'success', tone: 'ok' };
		case 'degraded':
			return { label: m.warpscout_state_degraded(), variant: 'warning', tone: 'warn' };
		case 'swapping':
			return { label: m.warpscout_state_swapping(), variant: 'info', tone: 'info' };
		case 'backoff':
			return { label: m.warpscout_state_backoff(), variant: 'warning', tone: 'warn' };
		case 'wan_down':
			return { label: m.warpscout_state_wan_down(), variant: 'error', tone: 'err' };
		case 'target_missing':
			return { label: m.warpscout_state_target_missing(), variant: 'error', tone: 'err' };
		case 'target_stopped':
			return { label: m.warpscout_state_target_stopped(), variant: 'muted', tone: 'muted' };
		case 'no_account':
			return { label: m.warpscout_state_no_account(), variant: 'error', tone: 'err' };
		case 'engine_missing':
			return { label: m.warpscout_state_engine_missing(), variant: 'error', tone: 'err' };
		case 'protected':
			return { label: m.warpscout_state_protected(), variant: 'error', tone: 'err' };
		case 'not_warp':
			return { label: m.warpscout_state_not_warp(), variant: 'error', tone: 'err' };
		case 'engine_busy':
			return { label: m.warpscout_state_engine_busy(), variant: 'warning', tone: 'warn' };
		default:
			return { label: state, variant: 'default', tone: 'muted' };
	}
}

export type LatencyTone = 'ok' | 'slow' | 'fail' | 'none';

/** Цветовой уровень задержки в мс для точек и значений пинга. */
export function latencyTone(ms: number | undefined | null): LatencyTone {
	if (!ms || ms <= 0) return 'none';
	if (ms <= 65) return 'ok';
	if (ms <= 140) return 'slow';
	return 'fail';
}

/** Сжимает сырые сетевые ошибки Go в короткую человекочитаемую причину автоподмены. */
export function humanizeSwapReason(raw: string | undefined | null): string {
	const s = (raw ?? '').trim();
	if (!s) return m.warpscout_reason_planned();
	const low = s.toLowerCase();
	if (low.startsWith('нет связи через туннель')) {
		if (low.includes('i/o timeout') || low.includes('deadline exceeded') || low.includes('timeout')) {
			return m.warpscout_reason_trace_timeout();
		}
		if (low.includes('connection reset') || low.includes('connection refused') || low.includes('eof')) {
			return m.warpscout_reason_conn_reset();
		}
		if (low.includes('network is unreachable') || low.includes('no route to host')) {
			return m.warpscout_reason_no_route();
		}
		return m.warpscout_reason_no_connectivity();
	}
	return s;
}

function errorLabelByCode(code: string): string | undefined {
	switch (code) {
		case 'WARP_NO_ACCOUNT':
			return m.warpscout_err_no_account();
		case 'WARP_BUSY':
			return m.warpscout_err_busy();
		case 'WARP_ENGINE_MISSING':
			return m.warpscout_err_engine_missing();
		case 'WARP_PROTECTED':
			return m.warpscout_err_protected();
		case 'WARP_NOT_WARP':
			return m.warpscout_err_not_warp();
		case 'OPERATION_IN_PROGRESS':
			return m.warpscout_err_op_in_progress();
		default:
			return undefined;
	}
}

/** Человекочитаемое сообщение об ошибке API (с учётом кода ошибки). */
export function warpErrorMessage(e: unknown): string {
	const err = e as { message?: string; code?: string } | null;
	const msg = (err?.message ?? String(e ?? '')).trim();
	const code = err?.code ?? '';
	if (code === 'INVALID_INPUT') return msg || m.warpscout_err_invalid_input();
	const label = errorLabelByCode(code);
	if (!label) return msg || m.warpscout_err_unknown();
	if (!msg || msg === label) return label;
	return `${label} (${msg})`;
}

/** Время из Go: нулевое `0001-01-01T00:00:00Z` считается отсутствующим. */
export function parseGoTime(s: string | undefined | null): Date | null {
	if (!s) return null;
	const d = new Date(s);
	if (Number.isNaN(d.getTime()) || d.getFullYear() < 2000) return null;
	return d;
}

export function formatTime(s: string | undefined | null): string {
	const d = parseGoTime(s);
	if (!d) return '—';
	const loc = formatLocale();
	const sameDay = new Date().toDateString() === d.toDateString();
	return sameDay
		? d.toLocaleTimeString(loc)
		: d.toLocaleString(loc, {
				day: '2-digit',
				month: '2-digit',
				hour: '2-digit',
				minute: '2-digit',
				second: '2-digit',
			});
}

export function formatDuration(ms: number): string {
	const total = Math.max(0, Math.round(ms / 1000));
	const mins = Math.floor(total / 60);
	const secs = total % 60;
	return mins > 0 ? m.warpscout_dur_min_sec({ m: mins, s: secs }) : m.warpscout_dur_sec({ s: secs });
}

/** Компактное время непрерывной работы узла (например: `45 с`, `18 мин`, `4 ч 12 мин`, `3 д 5 ч`). */
export function formatUptime(sinceIso: string | undefined | null, nowMs = Date.now()): string {
	const d = parseGoTime(sinceIso);
	if (!d) return '—';
	const sec = Math.max(0, Math.floor((nowMs - d.getTime()) / 1000));
	if (sec < 60) return m.warpscout_dur_sec({ s: sec });
	const min = Math.floor(sec / 60);
	if (min < 60) return m.warpscout_dur_min({ m: min });
	const hr = Math.floor(min / 60);
	const remMin = min % 60;
	if (hr < 24) return remMin > 0 ? m.warpscout_dur_hr_min({ h: hr, m: remMin }) : m.warpscout_dur_hr({ h: hr });
	const days = Math.floor(hr / 24);
	const remHr = hr % 24;
	return remHr > 0 ? m.warpscout_dur_day_hr({ d: days, h: remHr }) : m.warpscout_dur_day({ d: days });
}

/** Процент успешных проверок (например `99.4%` или `100%`). */
export function formatSlaPct(ok: number, total: number): string {
	if (!total || total <= 0) return '—';
	const pct = (Math.max(0, ok) / total) * 100;
	if (pct >= 99.95) return '100%';
	if (pct >= 99) return `${pct.toFixed(1)}%`;
	return `${Math.round(pct)}%`;
}

/** Сортированный список колокейшнов по числу попаданий (топ-N). */
export function topColos(
	colos: Record<string, number> | undefined | null,
	limit = 5,
): Array<{ colo: string; count: number; pct: number }> {
	if (!colos) return [];
	const entries = Object.entries(colos).filter(([, c]) => c > 0);
	if (entries.length === 0) return [];
	const sum = entries.reduce((acc, [, c]) => acc + c, 0);
	entries.sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));
	return entries.slice(0, limit).map(([colo, count]) => ({
		colo,
		count,
		pct: sum > 0 ? Math.round((count / sum) * 100) : 0,
	}));
}

export function shortId(id: string | undefined): string {
	if (!id) return '—';
	return id.length > 13 ? `${id.slice(0, 8)}…${id.slice(-4)}` : id;
}

export function defaultFilters(maxRttMs = 0): WarpFilters {
	return {
		protocol: 'awg',
		excludeColos: ['DME', 'LED'],
		allowedCountries: [],
		maxRttMs,
		requireTelegram: false,
	};
}

export function newProfileId(): string {
	// crypto.randomUUID недоступен вне secure context (UI роутера по http).
	let out = '';
	for (let i = 0; i < 12; i++) out += Math.floor(Math.random() * 16).toString(16);
	return out;
}

export function newProfile(index: number): WarpProfile {
	return {
		id: newProfileId(),
		name: `Профиль ${index}`,
		enabled: false,
		targetMode: 'kernel',
		targetId: '',
		filters: defaultFilters(250),
		checkIntervalSec: 60,
		failThreshold: 2,
		cooldownSec: 120,
	};
}

export function cloneConfig(c: WarpConfig): WarpConfig {
	return JSON.parse(JSON.stringify(c)) as WarpConfig;
}

export function targetKey(mode: string, id: string): string {
	return `${mode}:${id}`;
}

/** Цель доступна для автопилота / подмены. */
export function targetSelectable(t: WarpTunnelRef): boolean {
	return t.isWarp && !t.protected;
}

export function targetDisabledReason(t: WarpTunnelRef): string {
	if (t.protected) return t.protectReason || m.warpscout_target_protected_short();
	if (!t.isWarp) return m.warpscout_target_not_warp_short();
	return '';
}

export function targetOptionDescription(t: WarpTunnelRef): string {
	if (!targetSelectable(t)) return targetDisabledReason(t);
	const ep = t.endpoint || m.warpscout_target_no_endpoint();
	return t.running ? ep : m.warpscout_target_desc_stopped({ endpoint: ep });
}
