/**
 * Client-side AmneziaWG / WireGuard .conf heuristics (ported from standalone analyzer).
 * All parsing runs in the browser; nothing is sent to the API.
 */

export type AwgIface = Record<string, string>;

export type AwgParsed = { iface: AwgIface; peer: AwgIface };

export type AwgVersionInfo = {
	ver: string;
	desc: string;
};

export type CheckStatus = 'pass' | 'warn' | 'fail' | 'info';

export type AwgCheck = {
	cat: string;
	title: string;
	status: CheckStatus;
	value: string;
	detail: string;
	pts: number;
	max: number;
};

export type AwgScores = { total: number; dpi: number; stealth: number };

export type AwgVerdict = { label: string; color: string; tint: string; text: string };

export type AwgCamouflage = 'LOW' | 'MEDIUM' | 'HIGH';

function getInt(obj: AwgIface, key: string, def: number | null = null): number | null {
	const v = obj[key.toLowerCase()];
	if (v === undefined || v === null || v === '') return def;
	const n = parseInt(v, 10);
	return Number.isNaN(n) ? def : n;
}

function getStr(obj: AwgIface, key: string, def = ''): string {
	return (obj[key.toLowerCase()] || def).toString().trim();
}

function hasKey(obj: AwgIface, key: string): boolean {
	return obj[key.toLowerCase()] !== undefined;
}

export function parseAWG(raw: string): AwgParsed {
	const trimmed = raw.trim();
	if (!trimmed) throw new Error('Пустой конфиг');

	const cfg: { iface: Record<string, string>; peer: Record<string, string> } = {
		iface: {},
		peer: {},
	};
	let section: string | null = null;

	for (const line0 of trimmed.split('\n')) {
		const line = line0.trim();
		if (!line || line.startsWith('#')) continue;
		const sm = line.match(/^\[(\w+)\]$/);
		if (sm) {
			section = sm[1].toLowerCase();
			continue;
		}
		const em = line.match(/^([^=]+?)\s*=\s*(.*)$/);
		if (!em || !section) continue;
		const k = em[1].trim();
		const v = em[2].trim();
		if (section === 'interface') cfg.iface[k] = v;
		else if (section === 'peer') cfg.peer[k] = v;
	}

	if (!cfg.iface.PrivateKey && !cfg.iface.privatekey && !trimmed.includes('[Interface]')) {
		throw new Error('Не найдена секция [Interface]. Вставьте .conf файл AmneziaWG / WireGuard.');
	}

	const iface: AwgIface = {};
	for (const k of Object.keys(cfg.iface)) {
		iface[k.toLowerCase()] = cfg.iface[k];
	}
	const peer: AwgIface = {};
	for (const k of Object.keys(cfg.peer)) {
		peer[k.toLowerCase()] = cfg.peer[k];
	}

	return { iface, peer };
}

export function detectVersion(iface: AwgIface): AwgVersionInfo {
	const hasJc = hasKey(iface, 'jc');
	const hasS1 = hasKey(iface, 's1');
	const hasH1 = hasKey(iface, 'h1');
	const hasI1 = hasKey(iface, 'i1');
	const hasS3 = hasKey(iface, 's3');
	const hasS4 = hasKey(iface, 's4');

	const Jc = getInt(iface, 'jc', 0) ?? 0;
	const S1 = getInt(iface, 's1', 0) ?? 0;
	const S2 = getInt(iface, 's2', 0) ?? 0;

	function parseH(key: string, def: number) {
		const v = (iface[key.toLowerCase()] || '').toString().trim();
		if (!v) return { val: def, isRange: false };
		const rm = v.match(/^(\d+)-(\d+)$/);
		if (rm) return { val: parseInt(rm[1], 10), isRange: true };
		const n = parseInt(v, 10);
		return { val: Number.isNaN(n) ? def : n, isRange: false };
	}

	const h1 = parseH('h1', 1);
	const h2 = parseH('h2', 2);
	const h3 = parseH('h3', 3);
	const h4 = parseH('h4', 4);
	const headersHaveRanges = h1.isRange || h2.isRange || h3.isRange || h4.isRange;
	const headersDefault =
		!headersHaveRanges && h1.val === 1 && h2.val === 2 && h3.val === 3 && h4.val === 4;

	function getProto(): string {
		const i1 = getStr(iface, 'i1');
		if (i1.includes('0xc0') || i1.includes('0xC0')) return 'QUIC';
		if (i1.includes('160303') || i1.includes('0x1603')) return 'TLS';
		if (i1.toLowerCase().includes('register') || i1.toLowerCase().includes('sip')) return 'SIP';
		if (i1.includes('0x0001') || i1.includes('0x0000')) return 'DNS';
		return 'Custom';
	}

	const noObfuscation =
		(!hasJc || Jc === 0) && (!hasS1 || (S1 === 0 && S2 === 0)) && headersDefault && !hasI1;
	if (noObfuscation) {
		return {
			ver: 'WireGuard',
			desc: 'Стандартный WireGuard без обфускации. DPI легко обнаружит трафик.',
		};
	}

	if ((hasS3 || hasS4) && hasI1 && headersHaveRanges) {
		return {
			ver: 'AWG 2.0',
			desc: `AmneziaWG 2.0 — максимальная обфускация. H1-H4 диапазоны + S3/S4 рандомизируют все типы пакетов + CPS (I1 имитирует ${getProto()}).`,
		};
	}

	if (hasI1) {
		return {
			ver: 'AWG 1.5',
			desc: `AmneziaWG 1.5 — Jc/S1/S2/H1-H4 + CPS (I1 имитирует ${getProto()} handshake). Для AWG 2.0 нужны S3/S4 и H1-H4 в виде диапазонов.`,
		};
	}

	if (hasJc && Jc > 0) {
		return {
			ver: 'AWG 1.0',
			desc: 'AmneziaWG 1.0 — junk-пакеты (Jc/Jmin/Jmax) + S1/S2 рандомизируют размер handshake + кастомные H1-H4 скрывают сигнатуру WireGuard.',
		};
	}

	return {
		ver: 'AWG 1.0',
		desc: 'AmneziaWG 1.0 — минимальная обфускация.',
	};
}

/** Верхняя граница MTU на интерфейсе туннеля с учётом обфускации (полезная нагрузка / overhead). */
export function mtuCeilingForProfile(version: AwgVersionInfo): number {
	switch (version.ver) {
		case 'WireGuard':
			return 1420;
		case 'AWG 2.0':
			return 1320;
		case 'AWG 1.5':
			return 1360;
		case 'AWG 1.0':
		default:
			return 1380;
	}
}

/** Справочник MTU для блока рекомендаций (одним текстом с переносами строк). */
export function mtuCheatsheetRu(): string {
	return [
		'Справочник MTU (интерфейс туннеля):',
		'>1500 — В большинстве случаев не будет работать.',
		'1500 — Ethernet без дополнительного tunnel overhead',
		'1420 — Стандартный WireGuard',
		'1380 — Баланс / AWG 1.0',
		'1360 — Провайдеры с PPPoE overhead, AWG 1.5 + CPS',
		'1340 — Мобильный 4G/LTE',
		'1320 — AWG 2.0 + CPS (рекомендуется)',
		'1280 — Максимальная совместимость по пути',
	].join('\n');
}

export function runChecks(iface: AwgIface, peer: AwgIface, version: AwgVersionInfo): AwgCheck[] {
	const R: AwgCheck[] = [];
	const addChk = (
		cat: string,
		title: string,
		status: CheckStatus,
		value: unknown,
		detail: string,
		pts: number,
		max: number,
	) => {
		R.push({
			cat,
			title,
			status,
			value: String(value),
			detail,
			pts,
			max,
		});
	};

	const Jc = getInt(iface, 'jc', null);
	const Jmin = getInt(iface, 'jmin', null);
	const Jmax = getInt(iface, 'jmax', null);
	const S1 = getInt(iface, 's1', null);
	const S2 = getInt(iface, 's2', null);
	const S3 = getInt(iface, 's3', null);
	const S4 = getInt(iface, 's4', null);
	const I1 = getStr(iface, 'i1');
	const I2 = getStr(iface, 'i2');
	const I3 = getStr(iface, 'i3');

	const privkey = getStr(iface, 'privatekey');
	const isBase64 = (s: string) => /^[A-Za-z0-9+/]{43}=?$/.test(s);
	const pkOk = !!privkey && isBase64(privkey);
	addChk(
		'Ключи',
		'PrivateKey',
		pkOk ? 'pass' : 'fail',
		pkOk ? `${privkey.slice(0, 12)}…` : '(отсутствует)',
		pkOk ? 'Корректный base64 Curve25519 ключ' : 'Приватный ключ отсутствует или невалиден',
		pkOk ? 10 : 0,
		10,
	);

	const pubkey = getStr(peer, 'publickey');
	const pubOk = !!pubkey && isBase64(pubkey);
	addChk(
		'Ключи',
		'PublicKey (Peer)',
		pubOk ? 'pass' : 'fail',
		pubOk ? `${pubkey.slice(0, 12)}…` : '(отсутствует)',
		pubOk ? 'Публичный ключ сервера присутствует' : 'Публичный ключ сервера отсутствует',
		pubOk ? 8 : 0,
		8,
	);

	const psk = getStr(peer, 'presharedkey');
	const pskOk = !!psk && isBase64(psk);
	addChk(
		'Ключи',
		'PresharedKey',
		pskOk ? 'pass' : 'warn',
		pskOk ? `${psk.slice(0, 12)}…` : '(отсутствует)',
		pskOk
			? 'PresharedKey задан — дополнительный слой симметричного шифрования (post-quantum устойчивость)'
			: 'PresharedKey отсутствует — дополнительная защита от будущих квантовых атак не активна',
		pskOk ? 6 : 2,
		6,
	);

	const hasJc = Jc !== null;
	addChk(
		'Junk-пакеты',
		'Jc (количество junk-пакетов)',
		!hasJc ? 'fail' : Jc === 0 ? 'warn' : Jc >= 3 && Jc <= 10 ? 'pass' : 'warn',
		hasJc ? String(Jc) : '(не задан)',
		!hasJc
			? 'Jc не задан — нет junk-пакетов, хандшейк детектируется по размеру'
			: Jc === 0
				? 'Jc=0 — junk-пакеты отключены'
				: Jc >= 3 && Jc <= 10
					? `Jc=${Jc} — в рекомендуемом диапазоне 3-10 ✓`
					: Jc > 10
						? `Jc=${Jc} — слишком много, создаёт ненужный трафик (рекомендуется 3-10)`
						: `Jc=${Jc} — мало (рекомендуется 3-10)`,
		!hasJc ? 0 : Jc === 0 ? 0 : Jc >= 3 && Jc <= 10 ? 8 : 4,
		8,
	);

	if (hasJc && Jc !== null && Jc > 0) {
		const jminOk = Jmin !== null && Jmin >= 10 && Jmin <= 500;
		const jmaxOk = Jmax !== null && Jmax >= (Jmin ?? 0) && Jmax <= 1280;
		const jrangeOk = jminOk && jmaxOk && Jmax !== null && Jmin !== null && Jmax - Jmin >= 30;

		addChk(
			'Junk-пакеты',
			'Jmin/Jmax (диапазон размеров)',
			jrangeOk ? 'pass' : jminOk && jmaxOk ? 'warn' : 'fail',
			Jmin !== null && Jmax !== null ? `${Jmin}–${Jmax}` : '(не задан)',
			Jmin === null || Jmax === null
				? 'Jmin/Jmax не заданы — нужны при Jc>0'
				: !jmaxOk
					? `Jmax=${Jmax} должен быть ≤ 1280 (MTU)`
					: !jminOk
						? `Jmin=${Jmin} слишком мало (рекомендуется ≥50)`
						: Jmax - Jmin < 30
							? 'Диапазон Jmin-Jmax слишком мал — паттерн предсказуем'
							: `Jmin=${Jmin} Jmax=${Jmax} — хороший диапазон, размеры непредсказуемы`,
			jrangeOk ? 6 : jminOk && jmaxOk ? 3 : 0,
			6,
		);
	}

	const hasS1 = S1 !== null;
	const hasS2 = S2 !== null;
	const s1Ok = hasS1 && S1 >= 0 && S1 <= 64;
	const s2Ok = hasS2 && S2 >= 0 && S2 <= 64;
	const s1s2Conflict = hasS1 && hasS2 && S1 !== null && S2 !== null && S1 + 56 === S2;

	addChk(
		'Handshake Padding (S1/S2)',
		'S1 — Init prefix',
		!hasS1 ? 'info' : S1 === 0 ? 'warn' : s1Ok ? 'pass' : 'warn',
		hasS1 ? String(S1) : '(не задан)',
		!hasS1
			? 'S1 не задан — только в AWG 1.5/2.0'
			: S1 === 0
				? 'S1=0 — рандомный префикс Init-пакета отключён'
				: s1Ok
					? `S1=${S1} — в рекомендуемом диапазоне 0-64 ✓`
					: `S1=${S1} — вне рекомендуемого диапазона 0-64`,
		!hasS1 ? 1 : S1 === 0 ? 0 : s1Ok ? 6 : 3,
		6,
	);

	addChk(
		'Handshake Padding (S1/S2)',
		'S2 — Response prefix',
		!hasS2 ? 'info' : S2 === 0 ? 'warn' : s2Ok ? 'pass' : 'warn',
		hasS2 ? String(S2) : '(не задан)',
		!hasS2
			? 'S2 не задан — только в AWG 1.5/2.0'
			: S2 === 0
				? 'S2=0 — рандомный префикс Response-пакета отключён'
				: s2Ok
					? `S2=${S2} — в рекомендуемом диапазоне 0-64 ✓`
					: `S2=${S2} — вне рекомендуемого диапазона 0-64`,
		!hasS2 ? 1 : S2 === 0 ? 0 : s2Ok ? 6 : 3,
		6,
	);

	if (s1s2Conflict && S1 !== null && S2 !== null) {
		addChk(
			'Handshake Padding (S1/S2)',
			'S1+56 = S2 конфликт',
			'fail',
			`S1=${S1} S2=${S2}`,
			`S1+56=${S1 + 56} совпадает с S2=${S2} — правило AWG: S1+56 ≠ S2. Это делает пакеты предсказуемыми!`,
			0,
			0,
		);
	}

	if (S3 !== null || S4 !== null) {
		const s3Ok = S3 !== null && S3 >= 0 && S3 <= 64;
		const s4Ok = S4 !== null && S4 >= 0 && S4 <= 64;
		addChk(
			'Handshake Padding (S1/S2)',
			'S3/S4 (AWG 2.0 extended)',
			s3Ok && s4Ok ? 'pass' : 'warn',
			`S3=${S3 ?? '—'} S4=${S4 ?? '—'}`,
			'S3/S4 — расширенные префиксы AWG 2.0 для Cookie и Data пакетов. Максимальная непредсказуемость размеров.',
			s3Ok && s4Ok ? 4 : 2,
			4,
		);
	}

	function parseHcheck(key: string, def: number | null) {
		const v = (iface[key.toLowerCase()] || '').toString().trim();
		if (!v) return { val: def, isRange: false, raw: null as string | null };
		const rm = v.match(/^(\d+)-(\d+)$/);
		if (rm) return { val: parseInt(rm[1], 10), max: parseInt(rm[2], 10), isRange: true, raw: v };
		const n = parseInt(v, 10);
		return { val: Number.isNaN(n) ? def : n, isRange: false, raw: v };
	}

	const hc1 = parseHcheck('h1', null);
	const hc2 = parseHcheck('h2', null);
	const hc3 = parseHcheck('h3', null);
	const hc4 = parseHcheck('h4', null);
	const allHraw = [hc1, hc2, hc3, hc4];
	const hasAllH = allHraw.every((h) => h.raw !== null);
	const hHaveRanges = allHraw.some((h) => h.isRange);
	const allHvals = allHraw.map((h) => h.val).filter((v): v is number => v !== null);
	const defaultH =
		!hHaveRanges &&
		allHvals.length === 4 &&
		allHvals[0] === 1 &&
		allHvals[1] === 2 &&
		allHvals[2] === 3 &&
		allHvals[3] === 4;
	const uniqueH = hasAllH && new Set(allHvals).size === 4;
	const hDisplayVal = hasAllH
		? allHraw.map((h) => (h.isRange && h.raw ? h.raw : h.val)).join(' / ')
		: '(не заданы)';

	let hStatus: CheckStatus;
	let hPts: number;
	let hDetail: string;
	if (!hasAllH) {
		hStatus = 'warn';
		hPts = 0;
		hDetail = 'H1-H4 не заданы или неполные — WireGuard сигнатура хандшейка не скрыта';
	} else if (defaultH) {
		hStatus = 'fail';
		hPts = 0;
		hDetail = 'H1=1 H2=2 H3=3 H4=4 — ДЕФОЛТНЫЕ значения WireGuard! Хандшейк легко идентифицируется DPI.';
	} else if (!uniqueH) {
		hStatus = 'fail';
		hPts = 0;
		hDetail =
			'H1-H4 содержат повторяющиеся значения — конфигурация невалидна (все заголовки должны быть уникальны)';
	} else if (hHaveRanges) {
		hStatus = 'pass';
		hPts = 12;
		hDetail =
			'H1-H4 заданы диапазонами — AWG 2.0 режим, каждое соединение использует случайное значение из диапазона ✓';
	} else {
		hStatus = 'pass';
		hPts = 10;
		hDetail = 'H1-H4 уникальны — сигнатура хандшейка скрыта ✓';
	}

		addChk('Magic Headers (H1-H4)', 'H1-H4', hStatus, hDisplayVal, hDetail, hPts, 12);

	if (hHaveRanges) {
		const ranges = allHraw.map((h) => h.raw).filter((v): v is string => !!v && v.includes('-'));
		const smallRanges = ranges.filter((r) => {
			const p = r.split('-');
			return parseInt(p[1], 10) - parseInt(p[0], 10) < 1000;
		});
		if (smallRanges.length) {
			addChk(
				'Magic Headers (H1-H4)',
				'Диапазон H слишком мал',
				'warn',
				smallRanges.join(', '),
				'Маленький диапазон H уменьшает энтропию. Рекомендуется диапазон ≥1000 значений.',
				0,
				0,
			);
		}
	}

	if (hasAllH && uniqueH && !defaultH && !hHaveRanges) {
		const tooSmall = allHvals.filter((h) => h < 5);
		if (tooSmall.length) {
			addChk(
				'Magic Headers (H1-H4)',
				'H значения < 5',
				'warn',
				tooSmall.join(', '),
				'Значения H < 5 потенциально пересекаются с типами WireGuard (1-4). Рекомендуется H ≥ 5',
				0,
				0,
			);
		}
	}

	const hasI1 = !!I1;
	addChk(
		'CPS Мимикрий (I1-I5)',
		'I1 — Protocol Signature',
		hasI1 ? 'pass' : 'info',
		hasI1 ? `${I1.slice(0, 40)}${I1.length > 40 ? '…' : ''}` : '(не задан)',
		hasI1
			? `CPS активен — трафик имитирует реальный протокол. Параметр I1: ${I1.length} байт описания`
			: 'I1 не задан — AWG 2.0 мимикрий отключён. Для максимальной защиты добавьте I1 с QUIC или DNS handshake.',
		hasI1 ? 15 : 0,
		15,
	);

	if (hasI1) {
		const i1lower = I1.toLowerCase();
		const hex = i1lower.replace(/[^0-9a-f]/g, '');

		const isQuic =
			hex.startsWith('c0') || hex.includes('c0000001') || hex.includes('c3000000');
		const isTls = hex.startsWith('1603') || hex.includes('160301') || hex.includes('160303');
		const isDns =
			hex.includes('00010001') || hex.includes('000001000001') || hex.endsWith('00010001');
		const isHttp =
			hex.includes('474554') || hex.includes('504f5354') || hex.includes('48545450');
		const isStun = hex.startsWith('0001') && hex.includes('2112a442');
		const isDtls = hex.startsWith('16feff');

		const protoName = isQuic
			? 'QUIC Initial'
			: isTls
				? 'TLS ClientHello'
				: isDns
					? 'DNS Query'
					: isHttp
						? 'HTTP Request'
						: isStun
							? 'STUN'
							: isDtls
								? 'DTLS'
								: 'Custom';

		addChk(
			'CPS Мимикрий (I1-I5)',
			'Протокол имитации',
			'pass',
			protoName,
			isQuic
				? 'QUIC Initial handshake — идеальный выбор. QUIC — популярный протокол (YouTube, Google). DPI трудно отличить.'
				: isTls
					? 'TLS ClientHello — отлично маскируется под HTTPS трафик'
					: isDns
						? 'DNS Query имитация — трудно блокировать (порт 53 обычно открыт)'
						: 'Кастомный протокол. Убедитесь что hex-последовательность реалистична.',
			isQuic ? 5 : isDns ? 4 : 3,
			5,
		);

		const iCount = [I1, I2, I3, getStr(iface, 'i4'), getStr(iface, 'i5')].filter(Boolean).length;
		if (iCount > 1) {
			addChk(
				'CPS Мимикрий (I1-I5)',
				`Цепочка I1-I${iCount}`,
				'pass',
				`${iCount} пакетов`,
				`Задано ${iCount} сигнатурных пакетов — высокая энтропия сессии, счётчики и временные метки варьируются`,
				3,
				3,
			);
		}
	}

	const endpoint = getStr(peer, 'endpoint');
	if (endpoint) {
		const ep = endpoint.split(':');
		const epPort = parseInt(ep[ep.length - 1], 10);
		const epHost = ep
			.slice(0, -1)
			.join(':')
			.replace(/^\[|\]$/g, '');
		const portUdp = epPort === 51820 || epPort === 51821;

		addChk(
			'Сервер',
			'Endpoint порт',
			portUdp ? 'warn' : epPort < 1024 ? 'warn' : 'pass',
			String(epPort),
			portUdp
				? `${epPort} — стандартный WireGuard порт, легко идентифицируется DPI. Смените на любой выше 1024`
				: epPort === 53
					? '53 (DNS) — почти не блокируется, но может перехватываться DNS-прокси'
					: epPort < 1024
						? `${epPort} — системный порт (<1024), могут быть проблемы`
						: `${epPort} — нормально. Для AWG порт не критичен`,
			portUdp ? 2 : epPort < 1024 ? 2 : 5,
			5,
		);

		const isIPv4 = /^\d{1,3}(\.\d{1,3}){3}$/.test(epHost);
		const isIPv6 = epHost.includes(':');
		const isDomain = !isIPv4 && !isIPv6 && epHost.includes('.');

		addChk(
			'Сервер',
			'Endpoint адрес',
			isDomain ? 'pass' : isIPv4 || isIPv6 ? 'warn' : 'fail',
			epHost || '?',
			isDomain
				? 'Домен — удобнее при смене IP сервера'
				: isIPv4
					? 'IPv4 адрес — риск блокировки по IP'
					: isIPv6
						? 'IPv6 адрес — хорошо обходит некоторые блокировки по IPv4'
						: 'Неверный формат endpoint',
			isDomain ? 4 : isIPv4 || isIPv6 ? 2 : 0,
			4,
		);
	}

	const allowedIPs = getStr(peer, 'allowedips');
	const fullTunnel = allowedIPs.includes('0.0.0.0/0');
	const hasIPv6Tunnel = allowedIPs.includes('::/0');
	const splitTunnel = !!allowedIPs && !fullTunnel;

	addChk(
		'Маршрутизация',
		'AllowedIPs',
		fullTunnel && hasIPv6Tunnel ? 'pass' : fullTunnel ? 'warn' : splitTunnel ? 'info' : 'fail',
		allowedIPs || '(не задан)',
		fullTunnel && hasIPv6Tunnel
			? '0.0.0.0/0 + ::/0 — весь трафик (IPv4+IPv6) через VPN, минимальный DNS leak риск'
			: fullTunnel
				? '0.0.0.0/0 без ::/0 — IPv4 через VPN, IPv6 может утечь (DNS/WebRTC leak риск)'
				: splitTunnel
					? 'Split-tunneling — часть трафика мимо VPN (намеренно или нет)'
					: 'AllowedIPs не задан',
		fullTunnel && hasIPv6Tunnel ? 6 : fullTunnel ? 3 : splitTunnel ? 2 : 0,
		6,
	);

	const dns = getStr(iface, 'dns');
	const dnsOk = !!dns;
	const dnsPrivate =
		dns.includes('10.') || dns.includes('192.168.') || dns.includes('172.');
	const dnsPublicGood = ['1.1.1.1', '8.8.8.8', '9.9.9.9', '208.67.222.222', '94.140.14.14'].some(
		(d) => dns.includes(d),
	);
	addChk(
		'Маршрутизация',
		'DNS',
		dnsOk ? (dnsPrivate ? 'pass' : dnsPublicGood ? 'warn' : 'warn') : 'fail',
		dns || '(не задан)',
		!dnsOk
			? 'DNS не задан — запросы идут мимо VPN, полный DNS leak'
			: dnsPrivate
				? `DNS сервера VPN (${dns}) — запросы через туннель, нет утечки`
				: dnsPublicGood
					? `${dns} — публичный DNS через туннель, работает, но предпочтительнее DNS сервера VPN`
					: `${dns} — нестандартный DNS, убедитесь что запросы идут через туннель`,
		dnsOk ? (dnsPrivate ? 6 : dnsPublicGood ? 4 : 3) : 0,
		6,
	);

	const mtu = getInt(iface, 'mtu', null);
	const mtuCap = mtuCeilingForProfile(version);
	const mtuJmax = getInt(iface, 'jmax', null);
	if (mtu !== null) {
		const mtuConflict = mtuJmax !== null && mtuJmax >= mtu;
		let status: CheckStatus;
		let detail: string;
		let pts: number;
		const max = 4;

		if (mtu > 1500) {
			status = 'fail';
			detail = `MTU=${mtu} — значения >1500 некорректны для реального пути пакета.`;
			pts = 0;
		} else if (mtu < 1200) {
			status = 'fail';
			detail = `MTU=${mtu} слишком мало (<1200), высокий риск лишней фрагментации.`;
			pts = 0;
		} else if (mtuConflict) {
			status = 'fail';
			detail = `MTU=${mtu} но Jmax=${mtuJmax} ≥ MTU — junk-пакеты не помещаются, поведение подозрительно для DPI.`;
			pts = 0;
		} else if (mtu > mtuCap) {
			status = 'warn';
			detail = `MTU=${mtu} выше типичного потолка для «${version.ver}» (≤${mtuCap} с учётом обфускации и запаса под путь).`;
			pts = 2;
		} else {
			status = 'pass';
			detail = `MTU=${mtu} в диапазоне 1200–${mtuCap} для профиля «${version.ver}».`;
			pts = 4;
		}
		addChk('Сеть', 'MTU', status, String(mtu), detail, pts, max);
	} else {
		addChk(
			'Сеть',
			'MTU',
			'info',
			'(не задан)',
			`Явный MTU снижает сюрпризы на узких линках; для «${version.ver}» часто берут ≤${mtuCap} (см. рекомендации).`,
			0,
			0,
		);
	}

	const ka = getInt(peer, 'persistentkeepalive', null);
	if (ka !== null) {
		const kaOk = ka >= 15 && ka <= 60;
		addChk(
			'Сеть',
			'PersistentKeepalive',
			ka === 0 ? 'info' : kaOk ? 'pass' : 'warn',
			String(ka),
			ka === 0
				? 'Отключён — соединение может разорваться за NAT. Рекомендуется 20-35 секунд для стабильной работы'
				: kaOk
					? `${ka}s — хорошее значение, поддерживает NAT-соединение`
					: ka < 15
						? `${ka}s — слишком часто, создаёт лишний трафик (рекомендуется 15-60)`
						: `${ka}s — редко, NAT может закрыть соединение (рекомендуется 15-60)`,
			ka === 0 ? 3 : kaOk ? 3 : 1,
			3,
		);
	}

	return R;
}

export function calcScores(checks: AwgCheck[], iface: AwgIface, version: AwgVersionInfo): AwgScores {
	const tp = checks.reduce((a, c) => a + c.pts, 0);
	const mp = checks.reduce((a, c) => a + c.max, 0);
	const total = mp > 0 ? Math.round((tp / mp) * 100) : 0;

	let dpi = 95;
	if (version.ver.includes('2.0')) dpi -= 55;
	else if (version.ver.includes('1.5')) dpi -= 40;
	else if (version.ver.includes('1.0')) dpi -= 25;
	const Jc = getInt(iface, 'jc', 0) ?? 0;
	if (Jc >= 3) dpi -= 10;
	const H1 = getInt(iface, 'h1', 1) ?? 1;
	const H2 = getInt(iface, 'h2', 2) ?? 2;
	const H3 = getInt(iface, 'h3', 3) ?? 3;
	const H4 = getInt(iface, 'h4', 4) ?? 4;
	if (!(H1 === 1 && H2 === 2 && H3 === 3 && H4 === 4)) dpi -= 5;
	dpi = Math.max(3, Math.min(92, dpi));

	const stealth = Math.round(100 - dpi * 0.75);

	return { total, dpi, stealth };
}

export function buildFixes(checks: AwgCheck[], iface: AwgIface, peer: AwgIface, version: AwgVersionInfo): string[] {
	const fixes: string[] = [];

	const Jc = getInt(iface, 'jc', null);
	const Jmin = getInt(iface, 'jmin', null);
	const Jmax = getInt(iface, 'jmax', null);
	const S1 = getInt(iface, 's1', null);
	const S2 = getInt(iface, 's2', null);

	const H1 = getInt(iface, 'h1', null);
	const H2 = getInt(iface, 'h2', null);
	const H3 = getInt(iface, 'h3', null);
	const H4 = getInt(iface, 'h4', null);

	const dns = getStr(iface, 'dns');
	const allowed = getStr(peer, 'allowedips') || checks.find((c) => c.title === 'AllowedIPs')?.value || '';

	if (!dns) {
		fixes.push(
			'Добавьте DNS в [Interface] чтобы избежать DNS leak (например DNS = 1.1.1.1 или DNS сервера VPN).',
		);
	}

	if (!allowed.includes('::/0')) {
		fixes.push('Добавьте ::/0 в AllowedIPs чтобы закрыть IPv6 DNS/WebRTC leak.');
	}

	if (Jc !== null && Jc < 3) {
		fixes.push('Увеличьте Jc до диапазона 3–10 — больше junk‑пакетов повышает устойчивость к DPI.');
	}

	if (Jmin !== null && Jmax !== null && Jmax - Jmin < 30) {
		fixes.push('Увеличьте диапазон Jmin/Jmax (разница ≥30) чтобы размеры junk‑пакетов были менее предсказуемы.');
	}

	if (S1 !== null && S2 !== null && S1 + 56 === S2) {
		fixes.push('Измените S1 или S2 — правило AWG: S1 + 56 ≠ S2.');
	}

	if (version.ver === 'WireGuard') {
		fixes.push('Используйте AmneziaWG вместо чистого WireGuard — это добавит обфускацию и защиту от DPI.');
	}

	if (version.ver === 'AWG 1.5') {
		fixes.push('Для максимальной обфускации используйте AWG 2.0 (добавьте параметры S3 и S4).');
	}

	if (H1 === 1 && H2 === 2 && H3 === 3 && H4 === 4) {
		fixes.push('Измените H1‑H4 — значения 1‑4 являются сигнатурой стандартного WireGuard.');
	}

	const epCheck = checks.find((c) => c.title === 'Endpoint адрес');
	if (epCheck && epCheck.status === 'warn') {
		fixes.push(
			'Используйте доменное имя вместо IP в Endpoint — это усложняет блокировку по IP и позволяет менять сервер без обновления конфигов.',
		);
	}

	if (!getStr(iface, 'i1')) {
		fixes.push(
			'Добавьте CPS сигнатуру (I1) чтобы трафик имитировал реальный протокол (DNS, TLS или QUIC).',
		);
	}

	const mtuCap = mtuCeilingForProfile(version);
	const mtu = getInt(iface, 'mtu', null);
	let mtuTip = false;
	if (mtu === null) {
		mtuTip = true;
		fixes.push(
			`Задайте MTU = ${mtuCap} (или ниже по ситуации) — для «${version.ver}» это типичный потолок с учётом обфускации и запаса под путь.`,
		);
	} else if (mtu > 1500) {
		mtuTip = true;
		fixes.push(
			`Снизьте MTU: ${mtu} > 1500 некорректно для реального пути; ориентир ≤${mtuCap} для «${version.ver}».`,
		);
	} else if (mtu < 1200) {
		mtuTip = true;
		fixes.push(`Поднимите MTU минимум до 1200 (сейчас ${mtu}) — иначе пакеты будут слишком мелкими.`);
	} else if (mtu > mtuCap) {
		mtuTip = true;
		fixes.push(
			`Снизьте MTU с ${mtu} до ≤${mtuCap} для «${version.ver}»: при сильной обфускации полезная нагрузка меньше, необходимо учитывать запас под путь.`,
		);
	}

	if (mtuTip) {
		fixes.push(mtuCheatsheetRu());
	}

	return fixes;
}

export function getVerdict(score: number): AwgVerdict {
	if (score >= 88) {
		return {
			label: 'Максимальная защита',
			color: 'var(--color-accent, #a855f7)',
			tint: 'color-mix(in srgb, var(--color-accent, #a855f7) 18%, transparent)',
			text: 'AWG 2.0 с CPS мимикрием. Трафик неотличим от реального протокола. Высочайшая устойчивость к DPI и глубокому анализу.',
		};
	}
	if (score >= 70) {
		return {
			label: 'Хорошая защита',
			color: 'var(--color-success, #22c55e)',
			tint: 'var(--color-success-tint)',
			text: 'Надёжная конфигурация AWG. Есть параметры для улучшения — проверьте рекомендации.',
		};
	}
	if (score >= 50) {
		return {
			label: 'Средняя защита',
			color: 'var(--color-warning, #f59e0b)',
			tint: 'var(--color-warning-tint)',
			text: 'Базовая обфускация. Продвинутые DPI-системы могут идентифицировать трафик как AWG/WireGuard.',
		};
	}
	if (score >= 30) {
		return {
			label: 'Слабая защита',
			color: '#f97316',
			tint: 'color-mix(in srgb, #f97316 12%, transparent)',
			text: 'Минимальная обфускация. Трафик легко идентифицируется как WireGuard.',
		};
	}
	return {
		label: 'Стандартный WireGuard',
		color: 'var(--color-error, #ef4444)',
		tint: 'var(--color-error-tint)',
		text: 'Нет обфускации. DPI немедленно идентифицирует и может заблокировать трафик.',
	};
}

export function dpiLabel(d: number): { text: string; color: string } {
	if (d <= 20) return { text: 'LOW', color: 'var(--color-success, #22c55e)' };
	if (d <= 45) return { text: 'MEDIUM', color: 'var(--color-warning, #f59e0b)' };
	return { text: 'HIGH', color: 'var(--color-error, #ef4444)' };
}

export function camouflageFromI1(iface: AwgIface): AwgCamouflage {
	const i1 = (iface.i1 || '').toLowerCase();
	if (i1.includes('quic') || i1.includes('0xc0')) return 'HIGH';
	if (i1.includes('1603') || i1.includes('tls')) return 'HIGH';
	if (i1.includes('dns') || i1.includes('0001')) return 'MEDIUM';
	return 'LOW';
}

const CIRC = 2 * Math.PI * 50;

export function scoreRingDashArray(total: number): string {
	const pct = Math.min(100, Math.max(0, total));
	return `${(pct / 100) * CIRC} ${CIRC}`;
}
