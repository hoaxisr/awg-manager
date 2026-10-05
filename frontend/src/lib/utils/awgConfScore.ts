// Оценка качества обфускации поверх ответа POST /api/awg/analyze.
// Совместимость решает бэкенд; здесь — один балл 0–100 с обоснованием
// каждого штрафа и факты для плиток. Правила и цифры — по спеке
// docs/superpowers/specs/2026-09-07-awg-analyzer-rework-design.md §4.
import { m } from '$lib/i18n';
import type { AwgAnalyzeData, AwgVersionId } from '$lib/types';
import { parseI1, detectI1ProtocolFromHex } from './awgConfAnalyzer';

export type CheckStatus = 'pass' | 'warn' | 'fail' | 'info';

export type ScoreCheck = {
	cat: string;
	/** Стабильный код категории — для логики; cat — подпись на языке интерфейса. */
	catId: string;
	title: string;
	status: CheckStatus;
	value: string;
	detail: string;
	/** Вклад в балл: отрицательный — штраф, положительный — бонус, 0 — факт. */
	delta: number;
	/** Совет для блока «Рекомендации»; только у warn/fail. */
	fix?: string;
};

export type ScoreFacts = { profile: string; headerProtection: boolean; cps: string; trailers: boolean };
export type ScoreVerdict = { label: string; tone: 'error' | 'accent' | 'success' | 'warning' | 'muted'; text: string };
export type SummaryRow = { label: string; value: string };
export type ScoreResult = {
	score: number;
	base: number;
	checks: ScoreCheck[];
	verdict: ScoreVerdict;
	facts: ScoreFacts;
	summary: SummaryRow[];
};

const PROFILE_LABEL: Record<AwgVersionId, string> = {
	wg: 'WireGuard',
	'awg1.0': 'AWG 1.0',
	'awg1.5': 'AWG 1.5',
	'awg2.0': 'AWG 2.0',
	awg3: 'AWG 3.0',
	'awg3.1': 'AWG 3.1',
};

type HRange = { lo: number; hi: number; range: boolean } | null;

function parseH(v: string): HRange {
	const s = v.trim();
	if (!s) return null;
	const hit = /^(\d+)-(\d+)$/.exec(s);
	if (hit) return { lo: Number(hit[1]), hi: Number(hit[2]), range: true };
	const n = Number(s);
	return Number.isFinite(n) ? { lo: n, hi: n, range: false } : null;
}

/** Уровень обфускации без учёта таймеров/паддинга 3.0: awg3 без HP считается по тому, что реально на проводе. */
function obfuscationTier(d: AwgAnalyzeData): 'wg' | 'awg1.0' | 'awg1.5' | 'awg2.0' | 'hp' | 'hp+rt' {
	const i = d.interface;
	if (i.headerProtection) return i.randomTrailers ? 'hp+rt' : 'hp';
	if (d.version === 'awg1.0' || d.version === 'awg1.5' || d.version === 'awg2.0' || d.version === 'wg') return d.version;
	// awg3 / awg3.1 без header protection — как классифицирует бэкенд ниже 3.x.
	const hs = [i.h1, i.h2, i.h3, i.h4].map(parseH);
	if (hs.some((h) => h?.range)) return 'awg2.0';
	if ([i.i1, i.i2, i.i3, i.i4, i.i5].some(Boolean)) return 'awg1.5';
	if (hs.every((h) => h !== null)) return 'awg1.0';
	return 'wg';
}

const BASE: Record<ReturnType<typeof obfuscationTier>, number> = {
	wg: 0,
	'awg1.0': 40,
	'awg1.5': 55,
	'awg2.0': 65,
	hp: 85,
	'hp+rt': 95,
};

function cpsProtocol(i1: string): string {
	if (!i1) return m.awg_score_cps_none();
	if (/</.test(i1)) {
		const p = parseI1(i1).protocol;
		return p && p !== 'Unknown' ? p : 'Custom';
	}
	const hex = i1.toLowerCase().replace(/[^0-9a-f]/g, '');
	const p = detectI1ProtocolFromHex(hex);
	return p && p !== 'Unknown' ? p : 'Custom';
}

function endpointIsIPv6(endpoint: string): boolean {
	return endpoint.trim().startsWith('[');
}

function endpointPort(endpoint: string): number | null {
	const hit = /:(\d+)$/.exec(endpoint.trim());
	return hit ? Number(hit[1]) : null;
}

export function scoreConfig(d: AwgAnalyzeData): ScoreResult {
	const i = d.interface;
	const hp = i.headerProtection;
	const tier = obfuscationTier(d);
	const base = BASE[tier];
	const checks: ScoreCheck[] = [];
	const add = (c: ScoreCheck) => checks.push(c);

	// ── Заголовки H1–H4 ────────────────────────────────────────────
	const hs = [i.h1, i.h2, i.h3, i.h4].map(parseH);
	const hVal = [i.h1, i.h2, i.h3, i.h4].map((v) => v || '—').join(' / ');
	if (hp) {
		add({ cat: m.awg_score_cat_headers(), catId: 'headers', title: 'H1–H4', status: 'info', value: hVal, delta: 0,
			detail: m.awg_score_h_hp_detail() });
	} else {
		// Незаданный H модуль берёт из дефолта устройства 1/2/3/4 — так же
		// считает ValidateHeaderRanges на бэкенде.
		const singles = hs.map((h, idx) => h ?? { lo: idx + 1, hi: idx + 1, range: false });
		const isDefault = !singles.some((h) => h.range) && singles.map((h) => h.lo).join(',') === '1,2,3,4';
		const belowFive = singles.filter((h) => !h.range && h.lo < 5);
		const narrow = singles.filter((h) => h.range && h.hi - h.lo < 1000);
		if (isDefault) {
			add({ cat: m.awg_score_cat_headers(), catId: 'headers', title: 'H1–H4', status: 'fail', value: hVal, delta: -25,
				detail: m.awg_score_h_default_detail(),
				fix: m.awg_score_h_default_fix() });
		} else if (belowFive.length) {
			add({ cat: m.awg_score_cat_headers(), catId: 'headers', title: 'H1–H4', status: 'warn', value: hVal, delta: -5,
				detail: m.awg_score_h_below_five_detail({ values: belowFive.map((h) => h.lo).join(', ') }),
				fix: m.awg_score_h_below_five_fix() });
		} else if (narrow.length) {
			add({ cat: m.awg_score_cat_headers(), catId: 'headers', title: 'H1–H4', status: 'warn', value: hVal, delta: -5,
				detail: m.awg_score_h_narrow_detail(),
				fix: m.awg_score_h_narrow_fix() });
		} else {
			add({ cat: m.awg_score_cat_headers(), catId: 'headers', title: 'H1–H4', status: 'pass', value: hVal, delta: 0,
				detail: singles.some((h) => h.range) ? m.awg_score_h_pass_ranges() : m.awg_score_h_pass_unique() });
		}
	}

	// ── Junk ───────────────────────────────────────────────────────
	if (hp) {
		add({ cat: m.awg_score_cat_junk(), catId: 'junk', title: 'Jc', status: 'info', value: String(i.jc), delta: 0,
			detail: m.awg_score_jc_hp_detail() });
	} else if (i.jc <= 0) {
		add({ cat: m.awg_score_cat_junk(), catId: 'junk', title: 'Jc', status: tier === 'wg' ? 'info' : 'fail', value: String(i.jc), delta: tier === 'wg' ? 0 : -10,
			detail: m.awg_score_jc_zero_detail(),
			fix: m.awg_score_jc_zero_fix() });
	} else if (i.jc > 10) {
		add({ cat: m.awg_score_cat_junk(), catId: 'junk', title: 'Jc', status: 'warn', value: String(i.jc), delta: -5,
			detail: m.awg_score_jc_high_detail({ jc: i.jc }),
			fix: m.awg_score_jc_high_fix() });
	} else {
		add({ cat: m.awg_score_cat_junk(), catId: 'junk', title: 'Jc', status: 'pass', value: String(i.jc), delta: 0, detail: m.awg_score_jc_ok_detail({ jc: i.jc }) });
	}
	if (i.jc > 0) {
		if (hp) {
			add({ cat: m.awg_score_cat_junk(), catId: 'junk', title: 'Jmin/Jmax', status: 'info', value: `${i.jmin}–${i.jmax}`, delta: 0,
				detail: m.awg_score_jrange_hp_detail() });
		} else {
			const spread = i.jmax - i.jmin;
			if (spread < 30) {
				add({ cat: m.awg_score_cat_junk(), catId: 'junk', title: 'Jmin/Jmax', status: 'warn', value: `${i.jmin}–${i.jmax}`, delta: -5,
					detail: m.awg_score_jrange_narrow_detail(),
					fix: m.awg_score_jrange_narrow_fix() });
			} else {
				add({ cat: m.awg_score_cat_junk(), catId: 'junk', title: 'Jmin/Jmax', status: 'pass', value: `${i.jmin}–${i.jmax}`, delta: 0, detail: m.awg_score_jrange_ok_detail() });
			}
		}
	}

	// ── Паддинг S1–S4 ──────────────────────────────────────────────
	const sCat = m.awg_score_cat_padding();
	for (const [title, v] of [['S1', i.s1], ['S2', i.s2]] as const) {
		if (hp) {
			add({ cat: sCat, catId: 'padding', title, status: 'info', value: String(v), delta: 0, detail: m.awg_score_s_hp_detail() });
		} else if (tier === 'wg') {
			add({ cat: sCat, catId: 'padding', title, status: 'info', value: String(v), delta: 0, detail: m.awg_score_s_wg_detail() });
		} else if (v === 0) {
			add({ cat: sCat, catId: 'padding', title, status: 'warn', value: '0', delta: -5,
				detail: m.awg_score_s_zero_detail({ title, kind: title === 'S1' ? 'Init' : 'Response' }),
				fix: m.awg_score_s_zero_fix({ title }) });
		} else {
			add({ cat: sCat, catId: 'padding', title, status: 'pass', value: String(v), delta: 0, detail: m.awg_score_s_ok_detail() });
		}
	}
	if (!hp && i.s1 > 0 && i.s2 > 0 && i.s1 + 56 === i.s2) {
		add({ cat: sCat, catId: 'padding', title: 'S1 + 56 ≠ S2', status: 'fail', value: `${i.s1} + 56 = ${i.s2}`, delta: -10,
			detail: m.awg_score_s56_detail(),
			fix: m.awg_score_s56_fix() });
	}
	// Рекомендация Amnezia для 3.1 имеет смысл только с header protection; при
	// ошибке hp_padding_min бэкенд уже назвал те же значения — второй раз не
	// штрафуем (одна причина — один штраф).
	if (d.version === 'awg3.1' && hp && !d.errors.some((e) => e.code === 'hp_padding_min')) {
		const eq = i.s1 === i.s2 && i.s2 === i.s3 && i.s3 === i.s4 && i.s1 >= 12;
		add({ cat: sCat, catId: 'padding', title: 'S1 = S2 = S3 = S4 ≥ 12', status: eq ? 'pass' : 'fail',
			value: `${i.s1} / ${i.s2} / ${i.s3} / ${i.s4}`, delta: eq ? 0 : -15,
			detail: eq
				? m.awg_score_s31_ok_detail()
				: m.awg_score_s31_bad_detail(),
			fix: eq ? undefined : m.awg_score_s31_fix() });
	}

	// ── CPS ────────────────────────────────────────────────────────
	const cps = cpsProtocol(i.i1);
	if (i.i1) {
		if (/</.test(i.i1)) {
			const p = parseI1(i.i1);
			const ok = p.errors.length === 0;
			add({ cat: m.awg_score_cat_cps(), catId: 'cps', title: m.awg_score_i1_struct_title(), status: ok ? 'pass' : 'fail', value: cps, delta: ok ? 0 : -10,
				detail: ok ? m.awg_score_i1_ok_detail({ cps }) : p.errors.join(' '),
				fix: ok ? undefined : m.awg_score_i1_fix({ errors: p.errors.join(' ') }) });
		} else {
			add({ cat: m.awg_score_cat_cps(), catId: 'cps', title: m.awg_score_i1_struct_title(), status: 'pass', value: cps, delta: 0, detail: m.awg_score_i1_raw_detail({ cps }) });
		}
	} else {
		add({ cat: m.awg_score_cat_cps(), catId: 'cps', title: 'I1', status: 'info', value: m.awg_score_not_set(), delta: 0,
			detail: m.awg_score_i1_unset_detail() });
	}

	// ── Сервер ─────────────────────────────────────────────────────
	const port = endpointPort(d.peer.endpoint);
	if (port !== null) {
		const wgPort = port === 51820 || port === 51821;
		add({ cat: m.awg_score_cat_server(), catId: 'server', title: m.awg_score_port_title(), status: wgPort ? 'warn' : 'pass', value: String(port), delta: wgPort ? -5 : 0,
			detail: wgPort ? m.awg_score_port_wg_detail({ port }) : m.awg_score_port_ok_detail(),
			fix: wgPort ? m.awg_score_port_fix() : undefined });
	}

	// ── Сеть: MTU (совместимость, без баллов) ──────────────────────
	const overhead = endpointIsIPv6(d.peer.endpoint) ? 80 : 60;
	const ceiling = 1500 - overhead;
	// ContentPaddingAddition из потолка не вычитается: оба движка режут добавку
	// по свободному месту в UDP-окне (amneziawg-go send.go randomPaddingAddition,
	// модуль ядра peer.h wg_peer_skb_randomize_padding_addition).
	const pad = i.contentPaddingAddition;
	if (!i.mtuSet) {
		add({ cat: m.awg_score_cat_network(), catId: 'network', title: 'MTU', status: 'info', value: m.awg_score_not_set(), delta: 0,
			detail: pad ? m.awg_score_mtu_unset_pad_detail({ mtu: i.mtu, ceiling, overhead, pad }) : m.awg_score_mtu_unset_detail({ mtu: i.mtu, ceiling, overhead }) });
	} else if (i.mtu > 1500 || i.mtu < 1280) {
		add({ cat: m.awg_score_cat_network(), catId: 'network', title: 'MTU', status: 'fail', value: String(i.mtu), delta: 0,
			detail: i.mtu > 1500 ? m.awg_score_mtu_too_high_detail() : m.awg_score_mtu_too_low_detail(),
			fix: m.awg_score_mtu_range_fix({ ceiling }) });
	} else if (i.mtu > ceiling) {
		add({ cat: m.awg_score_cat_network(), catId: 'network', title: 'MTU', status: 'warn', value: String(i.mtu), delta: 0,
			detail: m.awg_score_mtu_above_detail({ ceiling, overhead }),
			fix: m.awg_score_mtu_above_fix({ ceiling, pppoe: ceiling - 8 }) });
	} else {
		add({ cat: m.awg_score_cat_network(), catId: 'network', title: 'MTU', status: 'pass', value: String(i.mtu), delta: 0, detail: pad ? m.awg_score_mtu_ok_pad_detail({ ceiling, pad }) : m.awg_score_mtu_ok_detail({ ceiling }) });
	}

	// ── Ключи (факты) ──────────────────────────────────────────────
	const pskLabel = d.peer.hasPresharedKey ? (d.peer.presharedKeyFromStore ? m.awg_score_psk_from_store() : m.awg_score_psk_set()) : m.awg_score_not_set();
	add({ cat: m.awg_score_cat_keys(), catId: 'keys', title: 'PresharedKey', status: 'info', value: pskLabel, delta: 0,
		detail: d.peer.hasPresharedKey
			? (d.peer.presharedKeyFromStore ? m.awg_score_psk_from_store_detail() : m.awg_score_psk_set_detail())
			: m.awg_score_psk_unset_detail() });

	// ── Совместимость (с бэкенда) ──────────────────────────────────
	// Без fix: одно сообщение — одно место. Ошибки уже показывает блок
	// «Конфиг не поднимется», предупреждения — сами чеки этой категории.
	for (const e of d.errors) {
		add({ cat: m.awg_score_cat_compat(), catId: 'compat', title: e.code, status: 'fail', value: m.awg_score_value_error(), delta: 0, detail: e.message });
	}
	for (const w of d.warnings) {
		add({ cat: m.awg_score_cat_compat(), catId: 'compat', title: w.code, status: 'warn', value: m.awg_score_value_warning(), delta: 0, detail: w.message });
	}

	// ── Итог ───────────────────────────────────────────────────────
	const score = Math.max(0, Math.min(100, base + checks.reduce((a, c) => a + c.delta, 0)));
	const facts: ScoreFacts = { profile: PROFILE_LABEL[d.version], headerProtection: hp, cps, trailers: i.randomTrailers };
	const noHp = (d.version === 'awg3' || d.version === 'awg3.1') && !hp;
	const profileText = m.awg_score_profile_text({
		profile: facts.profile,
		protection: hp ? 'hp' : noHp ? 'nohp' : 'none',
		trailers: i.randomTrailers ? 'yes' : 'no',
		cpsUsed: i.i1 ? 'yes' : 'no',
		cps,
	});
	let verdict: ScoreVerdict;
	if (d.errors.length) {
		verdict = { label: m.awg_score_verdict_error_label(), tone: 'error', text: m.awg_score_verdict_error_text() };
	} else if (score >= 85) {
		verdict = { label: m.awg_score_verdict_strong_label(), tone: 'accent', text: profileText };
	} else if (score >= 60) {
		verdict = { label: m.awg_score_verdict_good_label(), tone: 'success', text: m.awg_score_verdict_good_text({ profile: profileText }) };
	} else if (score >= 35) {
		verdict = { label: m.awg_score_verdict_basic_label(), tone: 'warning', text: m.awg_score_verdict_basic_text({ profile: profileText }) };
	} else {
		verdict = { label: m.awg_score_verdict_minimal_label(), tone: 'error', text: m.awg_score_verdict_minimal_text({ profile: profileText }) };
	}

	const byDefault = (v: string, set: boolean) => (set ? v : m.awg_score_by_default({ value: v }));
	const summary: SummaryRow[] = [
		{ label: m.awg_score_summary_profile(), value: facts.profile },
		{ label: 'Endpoint', value: d.peer.endpoint },
		{ label: 'AllowedIPs', value: byDefault(d.peer.allowedIPs.join(', '), d.peer.allowedIPsSet) },
		{ label: 'DNS', value: i.dns || '—' },
		{ label: 'MTU', value: i.mtuSet ? String(i.mtu) : m.awg_score_mtu_unset_value({ mtu: i.mtu }) },
		{ label: 'PersistentKeepalive', value: byDefault(d.peer.persistentKeepalive || '—', d.peer.keepaliveSet) },
		{ label: 'PresharedKey', value: pskLabel },
	];
	if (i.contentPaddingAddition) summary.push({ label: 'ContentPaddingAddition', value: i.contentPaddingAddition });
	for (const [label, v] of [['RekeyAfterTime', i.rekeyAfterTime], ['RekeyTimeout', i.rekeyTimeout], ['RejectAfterTime', i.rejectAfterTime], ['KeepaliveTimeout', i.keepaliveTimeout], ['MaxHandshakeAttempts', i.maxHandshakeAttempts]] as const) {
		if (v) summary.push({ label, value: v });
	}
	if (i.disableCookies) summary.push({ label: 'DisableCookies', value: 'on' });

	return { score, base, checks, verdict, facts, summary };
}

export function buildFixes(checks: ScoreCheck[]): string[] {
	return checks.filter((c) => c.fix && (c.status === 'fail' || c.status === 'warn')).map((c) => c.fix!);
}

const CIRC = 2 * Math.PI * 50;

export function scoreRingDashArray(total: number): string {
	const pct = Math.min(100, Math.max(0, total));
	return `${(pct / 100) * CIRC} ${CIRC}`;
}
