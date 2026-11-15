// Вспомогательные функции вкладки «WARP Сканер»: флаги, подписи
// состояний автопилота, разбор ошибок API и форматирование времени.
import type { BadgeVariant } from '$lib/components/ui';
import { m } from '$lib/i18n';
import type {
	WarpCheckSample,
	WarpConfig,
	WarpFilters,
	WarpFindJunkStatus,
	WarpHit,
	WarpProfile,
	WarpProfileMode,
	WarpRouteRef,
	WarpTargetMode,
	WarpTunnelRef,
} from '$lib/types/warpscout';
import { formatDate, formatDuration, formatTime as formatClock } from '$lib/utils/format';

export const QUICK_COLOS = ['LED', 'KJA', 'OVB', 'SVX'];
export const SIGNATURE_PROFILES = ['quic_initial', 'dns', 'stun', 'dtls', 'sip', 'none'] as const;
export const FIND_JUNK_PROFILES = ['random', 'quic_initial', 'dns', 'stun', 'dtls', 'sip', 'none'] as const;

export function hasUsableFindJunkBest(status: WarpFindJunkStatus | null | undefined): boolean {
	const b = status?.best;
	return Boolean(b && !b.incomplete && b.total > 0 && b.working > 0);
}

export function canApplyFindJunk(status: WarpFindJunkStatus | null | undefined): boolean {
	return Boolean(status && !status.running && hasUsableFindJunkBest(status));
}

export function signatureProfileLabel(prof: string | undefined): string {
	if (!prof || prof === 'none') return m.warpscout_sig_none();
	if (prof === 'custom') return m.warpscout_sig_custom();
	return prof;
}

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

/** Время проверки: сегодня — только часы, иначе дата и время; нулевое время Go — прочерк. */
export function formatTime(s: string | undefined | null): string {
	const d = parseGoTime(s);
	if (!d || !s) return '—';
	return new Date().toDateString() === d.toDateString() ? formatClock(s) : formatDate(s);
}

/** Длительность скана с секундами: общий formatDuration после минуты секунды отбрасывает. */
export function formatElapsed(ms: number): string {
	const total = Math.max(0, Math.round(ms / 1000));
	const mins = Math.floor(total / 60);
	const secs = total % 60;
	return mins > 0 ? m.warpscout_dur_min_sec({ m: mins, s: secs }) : m.warpscout_dur_sec({ s: secs });
}

/** Сколько узел работает без подмены; нулевое время Go — прочерк. */
export function formatUptime(sinceIso: string | undefined | null, nowMs = Date.now()): string {
	const d = parseGoTime(sinceIso);
	if (!d) return '—';
	return formatDuration(Math.max(0, Math.floor((nowMs - d.getTime()) / 1000)));
}

/** Процент успешных проверок (например `99.4%` или `100%`). */
export function formatSlaPct(ok: number, total: number): string {
	if (!total || total <= 0) return '—';
	const pct = (Math.max(0, ok) / total) * 100;
	if (pct >= 99.95) return '100%';
	if (pct >= 99) return `${pct.toFixed(1)}%`;
	return `${Math.round(pct)}%`;
}

/** Сортированный список дата-центров по числу попаданий (топ-N). */
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

// ── Числовые поля ────────────────────────────────────────────────

export interface NumLimit {
	def: number;
	min: number;
	max: number;
}

/**
 * Границы числовых полей — одни и для атрибутов min/max, и для санитайзера.
 * Сверены с normalizeProfile/normalizeConfig (internal/warpscout/store.go):
 * пауза <= 0 там превращается в 120, поэтому её минимум — 1.
 */
export const LIMITS = {
	checkIntervalSec: { def: 60, min: 15, max: 86400 },
	failThreshold: { def: 2, min: 1, max: 100 },
	cooldownSec: { def: 120, min: 1, max: 86400 },
	maxRttMs: { def: 0, min: 0, max: 10000 },
	jc: { def: 4, min: 0, max: 128 },
	jmin: { def: 40, min: 0, max: 1280 },
	jmax: { def: 70, min: 0, max: 1280 },
	maxHits: { def: 20, min: 1, max: 100 },
	findJunkAttempts: { def: 8, min: 1, max: 30 },
	findJunkThreshold: { def: 95, min: 50, max: 100 },
} as const satisfies Record<string, NumLimit>;

/** Целое в границах поля; пустое и нечисловое значение — значение по умолчанию. */
export function sanitizeInt(v: unknown, lim: NumLimit): number {
	if (v === null || v === undefined || v === '') return lim.def;
	const n = Math.round(Number(v));
	if (!Number.isFinite(n)) return lim.def;
	return Math.min(lim.max, Math.max(lim.min, n));
}

/** Конфигурация в том виде, в каком её можно отправлять на сервер. */
export function sanitizeConfig(cfg: WarpConfig): WarpConfig {
	const out = cloneConfig(cfg);
	out.scanInterface = (out.scanInterface ?? '').trim() || undefined;
	for (const p of out.profiles) {
		p.checkIntervalSec = sanitizeInt(p.checkIntervalSec, LIMITS.checkIntervalSec);
		p.failThreshold = sanitizeInt(p.failThreshold, LIMITS.failThreshold);
		p.cooldownSec = sanitizeInt(p.cooldownSec, LIMITS.cooldownSec);
		p.filters.maxRttMs = sanitizeInt(p.filters.maxRttMs, LIMITS.maxRttMs);
		p.filters.excludeColos ??= [];
		p.filters.allowedCountries ??= [];
	}
	out.awg.jc = sanitizeInt(out.awg.jc, LIMITS.jc);
	out.awg.jmin = sanitizeInt(out.awg.jmin, LIMITS.jmin);
	out.awg.jmax = sanitizeInt(out.awg.jmax, LIMITS.jmax);
	if (out.profileMode === 'single') {
		out.activeProfileId = out.profiles.find((p) => p.enabled)?.id;
	}
	return out;
}

// ── Черновик конфигурации ────────────────────────────────────────

/**
 * Части конфигурации с независимым «Сохранить»: профили автопилота и
 * параметры AWG. Маршрут сканера сохраняется сразу и черновика не имеет.
 */
export type ConfigSection = 'profiles' | 'awg';
const SECTIONS: ConfigSection[] = ['profiles', 'awg'];

// Форма для сравнения: сервер опускает пустой activeProfileId (omitempty),
// а пустые списки фильтров может отдать как null.
function sectionView(c: WarpConfig, s: ConfigSection): unknown {
	if (s === 'awg') {
		// У именованного пресета I1..I5 выдаёт сервер, интерфейс их не правит
		// и не шлёт: сравнивать их — значит видеть «не сохранено» после
		// выбора пресета и возврата к прежнему.
		if (c.awg.signatureProfile === 'custom') return c.awg;
		const view = { ...c.awg };
		delete view.i1;
		delete view.i2;
		delete view.i3;
		delete view.i4;
		delete view.i5;
		return view;
	}
	return {
		profileMode: c.profileMode,
		activeProfileId: c.activeProfileId || undefined,
		profiles: c.profiles.map((p) => ({
			...p,
			filters: {
				...p.filters,
				excludeColos: p.filters.excludeColos ?? [],
				allowedCountries: p.filters.allowedCountries ?? [],
			},
		})),
	};
}

export function sectionEqual(a: WarpConfig, b: WarpConfig, s: ConfigSection): boolean {
	return JSON.stringify(sectionView(a, s)) === JSON.stringify(sectionView(b, s));
}

/** `base`, в котором одна часть взята из `from`. */
export function withSection(base: WarpConfig, from: WarpConfig, s: ConfigSection): WarpConfig {
	const src = cloneConfig(from);
	if (s === 'awg') return { ...base, awg: src.awg };
	return {
		...base,
		profileMode: src.profileMode,
		activeProfileId: src.activeProfileId || undefined,
		profiles: src.profiles,
	};
}

/**
 * Принять серверную конфигурацию в черновик. Часть без локальных правок (и
 * любая из `force` — только что сохранённая или сброшенная) берётся с
 * сервера, часть с правками остаётся как есть. Возвращает тот же объект,
 * если менять нечего.
 */
export function syncDraft(
	draft: WarpConfig,
	prevServer: WarpConfig | null,
	nextServer: WarpConfig,
	force: ConfigSection[] = [],
): WarpConfig {
	if (!prevServer) return cloneConfig(nextServer);
	let out = draft;
	for (const s of SECTIONS) {
		const take = force.includes(s) || sectionEqual(draft, prevServer, s);
		if (take && !sectionEqual(out, nextServer, s)) out = withSection(out, nextServer, s);
	}
	return out;
}

/** Включить или выключить профиль; в режиме одного активного остальные гаснут. */
export function setProfileEnabled(config: WarpConfig, id: string, on: boolean): void {
	for (const p of config.profiles) {
		if (p.id === id) p.enabled = on;
		else if (config.profileMode === 'single') p.enabled = false;
	}
	if (config.profileMode === 'single') config.activeProfileId = on ? id : undefined;
}

/** Сменить режим; при переходе к одному активному остаётся прежний активный или первый включённый. */
export function setProfileMode(config: WarpConfig, mode: WarpProfileMode): void {
	config.profileMode = mode;
	if (mode !== 'single') return;
	const active =
		config.profiles.find((p) => p.id === config.activeProfileId && p.enabled) ??
		config.profiles.find((p) => p.enabled);
	for (const p of config.profiles) p.enabled = p === active;
	config.activeProfileId = active?.id;
}

// ── Лента проверок ───────────────────────────────────────────────

export const SPARK_SLOTS = 20;

/** Последние замеры, дополненные слева пустыми ячейками до ширины ленты. */
export function buildSparkBars(
	samples: WarpCheckSample[] | undefined,
	slots = SPARK_SLOTS,
): Array<WarpCheckSample | null> {
	const tail = (samples ?? []).slice(-slots);
	const pad = new Array<null>(Math.max(0, slots - tail.length)).fill(null);
	return [...pad, ...tail];
}

/**
 * Перцептивная высота столбика серии проверок (42–100%).
 * Сглаживает редкие многосекундные всплески, чтобы штатные 30–60 мс проверки
 * оставались крупными и спокойными на глаз.
 */
export function sampleBarHeight(s: WarpCheckSample): number {
	if (!s.ok) return 100;
	const ms = s.latencyMs ?? 0;
	if (ms <= 0) return 52;
	if (ms <= 20) return 42;
	if (ms <= 300) return 42 + Math.round(((ms - 20) / 280) * 40);
	return 82 + Math.round(Math.min(1, (ms - 300) / 1200) * 14);
}

export interface ParsedTunnelIPv4 {
	host: string;
	octets: [number, number, number, number];
	bits: number;
	normalized: string;
}

/** Разбирает пользовательский IPv4 туннеля (`172.16.0.3` или `172.16.0.3/32`). */
export function parseTunnelIpv4(raw: string | undefined | null): ParsedTunnelIPv4 | null {
	const s = (raw ?? '').trim();
	const match = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})(?:\/(\d{1,2}))?$/.exec(s);
	if (!match) return null;
	const parts = [match[1], match[2], match[3], match[4]];
	const nums: number[] = [];
	for (const p of parts) {
		const n = Number(p);
		if (!Number.isInteger(n) || n < 0 || n > 255 || String(n) !== p) return null;
		nums.push(n);
	}
	const [o0, o1, o2, o3] = nums as [number, number, number, number];
	if (o0 === 0 || o0 === 127 || o0 >= 224 || (o0 === 169 && o1 === 254)) return null;
	const bits = match[5] !== undefined ? Number(match[5]) : 32;
	if (
		!Number.isInteger(bits) ||
		bits < 1 ||
		bits > 32 ||
		(match[5] !== undefined && String(bits) !== match[5])
	) {
		return null;
	}
	const host = `${o0}.${o1}.${o2}.${o3}`;
	return {
		host,
		octets: [o0, o1, o2, o3],
		bits,
		normalized: bits === 32 ? host : `${host}/${bits}`,
	};
}

export function isValidTunnelIpv4(raw: string | undefined | null): boolean {
	return parseTunnelIpv4(raw) !== null;
}

export function normalizeIpv4Host(raw: string | undefined | null): string | null {
	return parseTunnelIpv4(raw)?.host ?? null;
}

export interface TunnelIpv4Conflict {
	conflict: boolean;
	host: string;
	tunnelName?: string;
}

/** Проверяет пересечение IPv4 с существующими туннелями и интерфейсами роутера. */
export function findTunnelIpv4Conflict(
	raw: string | undefined | null,
	targets: WarpTunnelRef[],
	usedIpv4?: string[] | null,
): TunnelIpv4Conflict {
	const parsed = parseTunnelIpv4(raw);
	if (!parsed) return { conflict: false, host: '' };
	for (const t of targets) {
		if (normalizeIpv4Host(t.addressV4) === parsed.host) {
			return {
				conflict: true,
				host: parsed.host,
				tunnelName: t.name || t.iface || t.id,
			};
		}
	}
	if ((usedIpv4 ?? []).some((ip) => normalizeIpv4Host(ip) === parsed.host)) {
		return { conflict: true, host: parsed.host };
	}
	return { conflict: false, host: parsed.host };
}

/** Подбирает ближайший свободный IPv4 в той же /24-подсети (или 172.16.0.x / 172.16.1.x). */
export function suggestFreeTunnelIpv4(
	raw: string | undefined | null,
	targets: WarpTunnelRef[],
	usedIpv4?: string[] | null,
): string {
	const occupied = new Set<string>();
	for (const t of targets) {
		const h = normalizeIpv4Host(t.addressV4);
		if (h) occupied.add(h);
	}
	for (const ip of usedIpv4 ?? []) {
		const h = normalizeIpv4Host(ip);
		if (h) occupied.add(h);
	}
	const parsed = parseTunnelIpv4(raw);
	const [a, b, c] = parsed ? parsed.octets : [172, 16, 0, 2];
	const bits = parsed?.bits ?? 32;
	const suffix = bits === 32 ? '' : `/${bits}`;
	for (let last = 2; last <= 254; last++) {
		const candidate = `${a}.${b}.${c}.${last}`;
		if (!occupied.has(candidate)) return candidate + suffix;
	}
	for (let sub = 1; sub <= 254; sub++) {
		for (let last = 2; last <= 254; last++) {
			const candidate = `172.16.${sub}.${last}`;
			if (!occupied.has(candidate)) return candidate + suffix;
		}
	}
	return parsed ? parsed.normalized : '172.16.0.2';
}

// ── Сканер ───────────────────────────────────────────────────────

/**
 * Результаты по возрастанию пинга, узлы без замера (pingMs = 0) — в конце.
 * Серверный sortHits ставит их первыми, поэтому порядок здесь свой: первая
 * строка получает метку TOP.
 */
export function sortHits(hits: WarpHit[] | null | undefined): WarpHit[] {
	const key = (h: WarpHit) => (h.pingMs > 0 ? h.pingMs : Number.MAX_SAFE_INTEGER);
	return [...(hits ?? [])].sort((a, b) => key(a) - key(b) || a.handshakeMs - b.handshakeMs);
}

function routeNamed(r: WarpRouteRef): string {
	return r.label && r.label !== r.name ? `${r.label} (${r.name})` : r.name;
}

/** Полная подпись маршрута сканера; пустой интерфейс — основной WAN. */
export function routeDisplayLabel(routes: WarpRouteRef[], defaultWan: string, ifname: string): string {
	if (!ifname) {
		const def = routes.find((r) => r.name === defaultWan);
		if (def) return m.warpscout_route_auto_named({ name: def.label || def.name });
		return defaultWan
			? m.warpscout_route_auto_named({ name: defaultWan })
			: m.warpscout_route_auto_default();
	}
	const found = routes.find((r) => r.name === ifname);
	return found ? routeNamed(found) : ifname;
}

/** Короткая подпись маршрута для чипа в шапке. */
export function routeChipText(routes: WarpRouteRef[], defaultWan: string, ifname: string): string {
	if (!ifname) {
		const def = routes.find((r) => r.name === defaultWan);
		if (def) return `WAN · ${routeNamed(def)}`;
		return defaultWan ? `WAN · ${defaultWan}` : m.warpscout_route_auto_default();
	}
	const found = routes.find((r) => r.name === ifname);
	return found ? routeNamed(found) : ifname;
}
