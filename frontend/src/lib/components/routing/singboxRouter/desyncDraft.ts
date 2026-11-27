import type { SingboxRouterDesyncFields } from '$lib/types';

// Списки значений — из option/desync.go awgm-box 46. Проверку значений и
// сочетаний делает sing-box check при сохранении, форма её не дублирует.
export const DESYNC_PRESETS = ['auto', 'custom', 'split', 'disorder', 'tlsrec', 'fake', 'fake_split', 'oob', 'GT1', 'GT2', 'GT3', 'GT4'];
export const DESYNC_MODES = ['none', 'split', 'disorder', 'tlsrec', 'fake', 'fake_split', 'oob', 'multi_split', 'fake_disorder', 'multi_disorder', 'disoob', 'hostfakesplit'];
export const DESYNC_FOOLING = ['none', 'badseq', 'badack', 'badsum', 'md5', 'md5_badack', 'badseq_badack_badsum', 'datanoack', 'hopbyhop', 'destopt', 'ipfrag', 'ts', 'badtimestamp'];
export const DESYNC_UDP_MODES = ['none', 'quic', 'stun', 'auto'];

/** Состояние формы: всё строками, пустая строка = «не задано, решает пресет». */
export interface DesyncDraft {
	preset: string;
	mode: string;
	split_pos: string;
	fake_sni: string;
	fake_sni_pool: string;
	fake_host: string;
	repeats: string;
	ttl: string;
	fooling: string;
	autottl: boolean;
	tlsrec_first: boolean;
	udp_mode: string;
	udp_ttl: string;
	udp_repeats: string;
	auto_tune: '' | 'on' | 'off';
	max_attempts: string;
	max_concurrent: string;
	max_entries: string;
	adaptive_ttl: string;
	adaptive_timeout: string;
	strategy: string;
}

type DesyncSource = SingboxRouterDesyncFields & { strategy?: string };

const str = (n: number | undefined) => (n === undefined ? '' : String(n));

export function desyncDraftFrom(o?: DesyncSource): DesyncDraft {
	const a = o?.adaptive ?? {};
	return {
		preset: o ? (o.preset ?? '') : 'auto',
		mode: o?.mode ?? '',
		split_pos: o?.split_pos ?? '',
		fake_sni: o?.fake_sni ?? '',
		fake_sni_pool: (o?.fake_sni_pool ?? []).join(', '),
		fake_host: o?.fake_host ?? '',
		repeats: str(o?.repeats),
		ttl: str(o?.ttl),
		fooling: o?.fooling ?? '',
		autottl: o?.autottl ?? false,
		tlsrec_first: o?.tlsrec_first ?? false,
		udp_mode: o?.udp_mode ?? '',
		udp_ttl: str(o?.udp_ttl),
		udp_repeats: str(o?.udp_repeats),
		auto_tune: o?.auto_tune === undefined ? '' : o.auto_tune ? 'on' : 'off',
		max_attempts: str(a.max_attempts),
		max_concurrent: str(a.max_concurrent),
		max_entries: str(a.max_entries),
		adaptive_ttl: a.ttl ?? '',
		adaptive_timeout: a.timeout ?? '',
		strategy: o?.strategy ?? '',
	};
}

/** Черновик → поля outbound'а; пустые не попадают. Бросает на нечисловом вводе. */
export function desyncFieldsFrom(d: DesyncDraft): DesyncSource {
	const out: DesyncSource = {};
	const text = (k: keyof DesyncSource & string, v: string) => {
		if (v.trim()) (out as Record<string, unknown>)[k] = v.trim();
	};
	const int = (name: string, v: string): number | undefined => {
		if (!v.trim()) return undefined;
		const n = Number(v.trim());
		if (!Number.isInteger(n)) throw new Error(`${name}: ${v}`);
		return n;
	};
	text('preset', d.preset);
	text('mode', d.mode);
	text('split_pos', d.split_pos);
	text('fake_sni', d.fake_sni);
	const pool = d.fake_sni_pool.split(',').map((s) => s.trim()).filter(Boolean);
	if (pool.length) out.fake_sni_pool = pool;
	text('fake_host', d.fake_host);
	out.repeats = int('repeats', d.repeats);
	out.ttl = int('ttl', d.ttl);
	text('fooling', d.fooling);
	if (d.autottl) out.autottl = true;
	if (d.tlsrec_first) out.tlsrec_first = true;
	text('udp_mode', d.udp_mode);
	out.udp_ttl = int('udp_ttl', d.udp_ttl);
	out.udp_repeats = int('udp_repeats', d.udp_repeats);
	if (d.auto_tune) out.auto_tune = d.auto_tune === 'on';
	const adaptive = {
		max_attempts: int('max_attempts', d.max_attempts),
		max_concurrent: int('max_concurrent', d.max_concurrent),
		max_entries: int('max_entries', d.max_entries),
		ttl: d.adaptive_ttl.trim() || undefined,
		timeout: d.adaptive_timeout.trim() || undefined,
	};
	if (Object.values(adaptive).some((v) => v !== undefined)) out.adaptive = adaptive;
	text('strategy', d.strategy);
	// undefined при JSON.stringify выпадает сам; чистим и для сравнения черновиков.
	return JSON.parse(JSON.stringify(out));
}
