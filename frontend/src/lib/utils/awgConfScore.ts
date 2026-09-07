// Оценка качества обфускации поверх ответа POST /api/awg/analyze.
// Совместимость решает бэкенд; здесь — один балл 0–100 с обоснованием
// каждого штрафа и факты для плиток. Правила и цифры — по спеке
// docs/superpowers/specs/2026-09-07-awg-analyzer-rework-design.md §4.
import type { AwgAnalyzeData, AwgVersionId } from '$lib/types';
import { parseI1, detectI1ProtocolFromHex } from './awgConfAnalyzer';

export type CheckStatus = 'pass' | 'warn' | 'fail' | 'info';

export type ScoreCheck = {
	cat: string;
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
	const m = /^(\d+)-(\d+)$/.exec(s);
	if (m) return { lo: Number(m[1]), hi: Number(m[2]), range: true };
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
	if (!i1) return 'нет';
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
	const m = /:(\d+)$/.exec(endpoint.trim());
	return m ? Number(m[1]) : null;
}

/** Верхняя граница ContentPaddingAddition: "0-64" → 64, "32" → 32, "" → 0. */
function paddingMax(v: string): number {
	const s = v.trim();
	if (!s) return 0;
	const m = /^(\d+)-(\d+)$/.exec(s);
	if (m) return Number(m[2]);
	const n = Number(s);
	return Number.isFinite(n) ? n : 0;
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
		add({ cat: 'Заголовки', title: 'H1–H4', status: 'info', value: hVal, delta: 0,
			detail: 'Заголовок зашифрован header protection — значения H1–H4 на проводе не видны, дефолт 1–4 допустим.' });
	} else if (hs.some((h) => h === null)) {
		add({ cat: 'Заголовки', title: 'H1–H4', status: 'info', value: hVal, delta: 0,
			detail: 'H1–H4 заданы не полностью — модуль использует свои значения по умолчанию.' });
	} else {
		const singles = hs.map((h) => h!);
		const isDefault = !singles.some((h) => h.range) && singles.map((h) => h.lo).join(',') === '1,2,3,4';
		const belowFive = singles.filter((h) => !h.range && h.lo < 5);
		const narrow = singles.filter((h) => h.range && h.hi - h.lo < 1000);
		if (isDefault) {
			add({ cat: 'Заголовки', title: 'H1–H4', status: 'fail', value: hVal, delta: -25,
				detail: 'H1=1 H2=2 H3=3 H4=4 — байты типа сообщения стандартного WireGuard, сигнатура рукопожатия видна DPI.',
				fix: 'Задайте уникальные H1–H4 (≥ 5) или диапазоны вида 10000-20000 — значения 1–4 являются сигнатурой WireGuard.' });
		} else if (belowFive.length) {
			add({ cat: 'Заголовки', title: 'H1–H4', status: 'warn', value: hVal, delta: -5,
				detail: `Значения ${belowFive.map((h) => h.lo).join(', ')} пересекаются с типами сообщений WireGuard 1–4.`,
				fix: 'Поднимите H-значения до ≥ 5 — 1–4 совпадают с типами сообщений WireGuard.' });
		} else if (narrow.length) {
			add({ cat: 'Заголовки', title: 'H1–H4', status: 'warn', value: hVal, delta: -5,
				detail: 'Есть H-диапазон уже 1000 значений — низкая энтропия заголовка.',
				fix: 'Расширьте H-диапазоны минимум до 1000 значений.' });
		} else {
			add({ cat: 'Заголовки', title: 'H1–H4', status: 'pass', value: hVal, delta: 0,
				detail: singles.some((h) => h.range) ? 'H-диапазоны без пересечений — значение меняется от сессии к сессии.' : 'H1–H4 уникальны и не совпадают с типами WireGuard.' });
		}
	}

	// ── Junk ───────────────────────────────────────────────────────
	if (hp) {
		add({ cat: 'Junk-пакеты', title: 'Jc', status: 'info', value: String(i.jc), delta: 0,
			detail: 'При header protection размер рукопожатия не выдаёт протокол — junk-пакеты не обязательны.' });
	} else if (i.jc <= 0) {
		add({ cat: 'Junk-пакеты', title: 'Jc', status: tier === 'wg' ? 'info' : 'fail', value: String(i.jc), delta: tier === 'wg' ? 0 : -10,
			detail: 'Junk-пакетов нет — рукопожатие детектируется по размеру.',
			fix: 'Задайте Jc в диапазоне 3–10 с Jmin/Jmax — junk-пакеты маскируют размер рукопожатия.' });
	} else if (i.jc > 10) {
		add({ cat: 'Junk-пакеты', title: 'Jc', status: 'warn', value: String(i.jc), delta: -5,
			detail: `Jc=${i.jc} — лишний трафик на каждое рукопожатие без прироста стойкости.`,
			fix: 'Снизьте Jc до 3–10.' });
	} else {
		add({ cat: 'Junk-пакеты', title: 'Jc', status: 'pass', value: String(i.jc), delta: 0, detail: `Jc=${i.jc} — в рекомендуемом диапазоне 3–10.` });
	}
	if (i.jc > 0) {
		if (hp) {
			add({ cat: 'Junk-пакеты', title: 'Jmin/Jmax', status: 'info', value: `${i.jmin}–${i.jmax}`, delta: 0,
				detail: 'При header protection размеры junk-пакетов не выдают протокол.' });
		} else {
			const spread = i.jmax - i.jmin;
			if (spread < 30) {
				add({ cat: 'Junk-пакеты', title: 'Jmin/Jmax', status: 'warn', value: `${i.jmin}–${i.jmax}`, delta: -5,
					detail: 'Разброс размеров junk-пакетов меньше 30 байт — размер предсказуем.',
					fix: 'Расширьте Jmin/Jmax так, чтобы разница была ≥ 30 байт.' });
			} else {
				add({ cat: 'Junk-пакеты', title: 'Jmin/Jmax', status: 'pass', value: `${i.jmin}–${i.jmax}`, delta: 0, detail: 'Размеры junk-пакетов непредсказуемы.' });
			}
		}
	}

	// ── Паддинг S1–S4 ──────────────────────────────────────────────
	const sCat = 'Паддинг (S1–S4)';
	for (const [title, v] of [['S1', i.s1], ['S2', i.s2]] as const) {
		if (hp) {
			add({ cat: sCat, title, status: 'info', value: String(v), delta: 0, detail: 'Первые 12 байт паддинга несут nonce header protection.' });
		} else if (tier === 'wg') {
			add({ cat: sCat, title, status: 'info', value: String(v), delta: 0, detail: 'Паддинг рукопожатия — параметр AmneziaWG; у чистого WireGuard его нет.' });
		} else if (v === 0) {
			add({ cat: sCat, title, status: 'warn', value: '0', delta: -5,
				detail: `${title}=0 — размер ${title === 'S1' ? 'Init' : 'Response'}-пакета совпадает со стандартным WireGuard.`,
				fix: `Задайте ${title} > 0 — иначе размер рукопожатия совпадает с WireGuard.` });
		} else {
			add({ cat: sCat, title, status: 'pass', value: String(v), delta: 0, detail: 'Размер пакета рукопожатия изменён.' });
		}
	}
	if (!hp && i.s1 > 0 && i.s2 > 0 && i.s1 + 56 === i.s2) {
		add({ cat: sCat, title: 'S1 + 56 ≠ S2', status: 'fail', value: `${i.s1} + 56 = ${i.s2}`, delta: -10,
			detail: 'Правило Amnezia: S1 + 56 не должно равняться S2, иначе размеры Init и Response связаны.',
			fix: 'Измените S1 или S2 так, чтобы S1 + 56 ≠ S2.' });
	}
	// Рекомендация Amnezia для 3.1 имеет смысл только с header protection; при
	// ошибке hp_padding_min бэкенд уже назвал те же значения — второй раз не
	// штрафуем (одна причина — один штраф).
	if (d.version === 'awg3.1' && hp && !d.errors.some((e) => e.code === 'hp_padding_min')) {
		const eq = i.s1 === i.s2 && i.s2 === i.s3 && i.s3 === i.s4 && i.s1 >= 12;
		add({ cat: sCat, title: 'S1 = S2 = S3 = S4 ≥ 12', status: eq ? 'pass' : 'fail',
			value: `${i.s1} / ${i.s2} / ${i.s3} / ${i.s4}`, delta: eq ? 0 : -15,
			detail: eq
				? 'Рекомендация Amnezia для AWG 3.1 выполнена. Учтите: паддинг транспортных пакетов (S4) заметно снижает скорость туннеля.'
				: 'Рекомендация Amnezia для AWG 3.1: S1–S4 равны и не меньше 12.',
			fix: eq ? undefined : 'Для AWG 3.1 задайте S1 = S2 = S3 = S4 ≥ 12 (рекомендация Amnezia).' });
	}

	// ── CPS ────────────────────────────────────────────────────────
	const cps = cpsProtocol(i.i1);
	if (i.i1) {
		if (/</.test(i.i1)) {
			const p = parseI1(i.i1);
			const ok = p.errors.length === 0;
			add({ cat: 'CPS (I1–I5)', title: 'Структура I1', status: ok ? 'pass' : 'fail', value: cps, delta: ok ? 0 : -10,
				detail: ok ? `Теги <b>/<r> корректны, первый пакет имитирует ${cps}.` : p.errors.join(' '),
				fix: ok ? undefined : 'Исправьте теги I1: ' + p.errors.join(' ') });
		} else {
			add({ cat: 'CPS (I1–I5)', title: 'Структура I1', status: 'pass', value: cps, delta: 0, detail: `Сырой hex, похож на ${cps}.` });
		}
		const chain = [i.i2, i.i3, i.i4, i.i5].filter(Boolean).length;
		if (chain > 0) {
			add({ cat: 'CPS (I1–I5)', title: 'Цепочка I2–I5', status: 'pass', value: `${chain} доп. пакетов`, delta: 2 * chain,
				detail: `+2 за каждый дополнительный сигнатурный пакет (${chain}).` });
		}
	} else {
		add({ cat: 'CPS (I1–I5)', title: 'I1', status: 'info', value: 'не задан', delta: 0,
			detail: 'Мимикрия первого пакета не используется. Опционально: I1 с QUIC/TLS/DNS.' });
	}

	// ── Сервер ─────────────────────────────────────────────────────
	const port = endpointPort(d.peer.endpoint);
	if (port !== null) {
		const wgPort = port === 51820 || port === 51821;
		add({ cat: 'Сервер', title: 'Порт Endpoint', status: wgPort ? 'warn' : 'pass', value: String(port), delta: wgPort ? -5 : 0,
			detail: wgPort ? `${port} — стандартный порт WireGuard.` : 'Порт не совпадает со стандартным портом WireGuard.',
			fix: wgPort ? 'Смените порт сервера с 51820/51821 на любой другой.' : undefined });
	}

	// ── Сеть: MTU (совместимость, без баллов) ──────────────────────
	const overhead = endpointIsIPv6(d.peer.endpoint) ? 80 : 60;
	const pad = paddingMax(i.contentPaddingAddition);
	const ceiling = 1500 - overhead - pad;
	if (!i.mtuSet) {
		add({ cat: 'Сеть', title: 'MTU', status: 'info', value: 'не задан', delta: 0, detail: `Будет взято ${i.mtu} по умолчанию. Потолок для этого конфига ${ceiling} (1500 − ${overhead} внешний заголовок${pad ? ` − ${pad} ContentPadding` : ''}); для PPPoE ещё −8.` });
	} else if (i.mtu > 1500 || i.mtu < 1280) {
		add({ cat: 'Сеть', title: 'MTU', status: 'fail', value: String(i.mtu), delta: 0,
			detail: i.mtu > 1500 ? 'MTU > 1500 не пройдёт по Ethernet-пути.' : 'MTU < 1280 ниже минимума IPv6 и даёт лишнюю фрагментацию.',
			fix: `Задайте MTU в диапазоне 1280–${ceiling}.` });
	} else if (i.mtu > ceiling) {
		add({ cat: 'Сеть', title: 'MTU', status: 'warn', value: String(i.mtu), delta: 0,
			detail: `Выше потолка ${ceiling} = 1500 − ${overhead} внешний заголовок${pad ? ` − ${pad} ContentPadding` : ''}; на PPPoE потолок ещё на 8 ниже.`,
			fix: `Снизьте MTU до ${ceiling} или ниже (PPPoE: ${ceiling - 8}).` });
	} else {
		add({ cat: 'Сеть', title: 'MTU', status: 'pass', value: String(i.mtu), delta: 0, detail: `В пределах потолка ${ceiling}.` });
	}

	// ── Ключи (факты) ──────────────────────────────────────────────
	const pskLabel = d.peer.hasPresharedKey ? (d.peer.presharedKeyFromStore ? 'задан (из туннеля)' : 'задан') : 'не задан';
	add({ cat: 'Ключи', title: 'PresharedKey', status: 'info', value: pskLabel, delta: 0,
		detail: d.peer.hasPresharedKey
			? (d.peer.presharedKeyFromStore ? 'В тексте ключа нет, взят из сохранённого туннеля — при записи он сохранится.' : 'Симметричный ключ поверх DH задан.')
			: 'Опциональный симметричный ключ поверх DH не задан.' });

	// ── Совместимость (с бэкенда) ──────────────────────────────────
	for (const e of d.errors) {
		add({ cat: 'Совместимость', title: e.code, status: 'fail', value: 'ошибка', delta: 0, detail: e.message, fix: e.message });
	}
	for (const w of d.warnings) {
		add({ cat: 'Совместимость', title: w.code, status: 'warn', value: 'предупреждение', delta: 0, detail: w.message, fix: w.message });
	}

	// ── Итог ───────────────────────────────────────────────────────
	const score = Math.max(0, Math.min(100, base + checks.reduce((a, c) => a + c.delta, 0)));
	const facts: ScoreFacts = { profile: PROFILE_LABEL[d.version], headerProtection: hp, cps, trailers: i.randomTrailers };
	const profileText = `${facts.profile}${hp ? ' с header protection' : ''}${i.randomTrailers ? ' и random trailers' : ''}${cps !== 'нет' ? `, CPS: ${cps}` : ''}.`;
	let verdict: ScoreVerdict;
	if (d.errors.length) {
		verdict = { label: 'Конфиг не поднимется', tone: 'error', text: d.errors[0].message };
	} else if (score >= 85) {
		verdict = { label: 'Сильная обфускация', tone: 'accent', text: profileText };
	} else if (score >= 60) {
		verdict = { label: 'Хорошая обфускация', tone: 'success', text: profileText + ' Есть что усилить — см. рекомендации.' };
	} else if (score >= 35) {
		verdict = { label: 'Базовая обфускация', tone: 'warning', text: profileText + ' Продвинутый DPI может распознать AWG.' };
	} else {
		verdict = { label: 'Минимальная или нет обфускации', tone: 'error', text: profileText + ' Трафик распознаётся как WireGuard.' };
	}

	const byDefault = (v: string, set: boolean) => (set ? v : `${v} (по умолчанию)`);
	const summary: SummaryRow[] = [
		{ label: 'Профиль', value: facts.profile },
		{ label: 'Endpoint', value: d.peer.endpoint },
		{ label: 'AllowedIPs', value: byDefault(d.peer.allowedIPs.join(', '), d.peer.allowedIPsSet) },
		{ label: 'DNS', value: i.dns || '—' },
		{ label: 'MTU', value: i.mtuSet ? String(i.mtu) : `не задан (по умолчанию ${i.mtu})` },
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
