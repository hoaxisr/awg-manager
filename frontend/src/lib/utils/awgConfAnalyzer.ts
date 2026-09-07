/**
 * Разбор .conf для записи в туннель и разбор тегов I1.
 * Оценка — в awgConfScore.ts, совместимость — на бэкенде.
 */

export type AwgIface = Record<string, string>;

export type AwgParsed = { iface: AwgIface; peer: AwgIface };

export type I1Parsed = {
	tags: { tag: string; arg: string }[];
	firstTag: string | null;
	hexData: string;
	rTagSizes: number[];
	rcTagSizes: number[];
	totalRBytes: number;
	hasT: boolean;
	hasC: boolean;
	hasRc: boolean;
	firstByte: number | null;
	firstByteOk: boolean;
	startsWith_b: boolean;
	maxRSize: number;
	errors: string[];
	protocol: string;
};

/** Распознавание протокола по hex после тега `<b 0x…>` (логика близка к pumbax/awg-analyzer). */
export function detectI1ProtocolFromHex(hex: string): string {
	if (!hex) return 'Unknown';
	const h = hex.toLowerCase();
	if (/^16030[0-3]/.test(h)) return 'TLS';
	if (/^16fefd/.test(h) || /^16feff/.test(h)) return 'DTLS';
	const knownQuic = ['00000001', '6b3343cf', 'ff00001d', 'ff00001e'];
	if (h.length >= 10) {
		const fb = parseInt(h.substring(0, 2), 16);
		const ver = h.substring(2, 10);
		if (fb >= 0xc0 && fb <= 0xef && (knownQuic.includes(ver) || h.startsWith('c0000'))) return 'QUIC';
	}
	const sipHex = [
		'494e56495445',
		'5245474953544552',
		'4f5054494f4e53',
		'4d455353414745',
		'5355425343524942',
		'4e4f54494659',
		'535542',
	];
	if (sipHex.some((s) => h.startsWith(s))) return 'SIP';
	if (/^[0-9a-f]{4}01[02]0000[12]/.test(h)) return 'DNS';
	if (h.startsWith('474554') || h.startsWith('504f5354') || h.startsWith('48545450')) return 'HTTP';
	if (h.startsWith('0001') && h.includes('2112a442')) return 'STUN';
	return 'Custom';
}

/** Разбор CPS-строки I1 с тегами `<b>`, `<r>`, … (pumbax/awg-analyzer). */
export function parseI1(i1: string): I1Parsed {
	const result: I1Parsed = {
		tags: [],
		firstTag: null,
		hexData: '',
		rTagSizes: [],
		rcTagSizes: [],
		totalRBytes: 0,
		hasT: false,
		hasC: false,
		hasRc: false,
		firstByte: null,
		firstByteOk: true,
		startsWith_b: false,
		maxRSize: 0,
		errors: [],
		protocol: 'Unknown',
	};

	const tagRegex = /<(\w+)(?:\s+([^>]*))?>/g;
	let m: RegExpExecArray | null;
	while ((m = tagRegex.exec(i1)) !== null) {
		const tag = m[1].toLowerCase();
		const arg = (m[2] || '').trim();
		result.tags.push({ tag, arg });

		if (tag === 'b') {
			const hexMatch = arg.match(/0x([0-9a-fA-F]+)/);
			if (hexMatch) result.hexData = hexMatch[1].toLowerCase();
		} else if (tag === 'r') {
			const n = parseInt(arg, 10);
			if (!Number.isNaN(n)) {
				result.rTagSizes.push(n);
				result.totalRBytes += n;
				if (n > result.maxRSize) result.maxRSize = n;
			}
		} else if (tag === 'rc') {
			result.hasRc = true;
			const n = parseInt(arg, 10);
			if (!Number.isNaN(n)) result.rcTagSizes.push(n);
		} else if (tag === 't') {
			result.hasT = true;
		} else if (tag === 'c') {
			result.hasC = true;
		}
	}

	if (result.tags.length === 0) {
		return result;
	}

	result.firstTag = result.tags[0].tag;
	result.startsWith_b = result.firstTag === 'b';
	if (!result.startsWith_b) {
		result.errors.push(
			'Первый тег в I1 должен быть <b …> — иначе парсер amneziawg-go может отказать в handshake.',
		);
	}

	for (const n of result.rTagSizes) {
		if (n >= 1000) {
			result.errors.push(
				`Тег <r> с размером ${n} ≥ 1000 — разбейте на части ≤999, иначе риск поломки парсера.`,
			);
		}
	}

	if (result.hasC) {
		result.errors.push('Тег <c> устарел — на старых клиентах AmneziaVPN возможен ErrorCode 1000.');
	}

	result.protocol = detectI1ProtocolFromHex(result.hexData);
	if (result.hexData.length >= 2) {
		const firstByte = parseInt(result.hexData.substring(0, 2), 16);
		result.firstByte = firstByte;

		if (result.protocol === 'QUIC') {
			result.firstByteOk = firstByte >= 0xc0 && firstByte <= 0xef;
			if (!result.firstByteOk) {
				result.errors.push(
					`Первый байт 0x${firstByte.toString(16)} — для QUIC long header ожидается 0xC0–0xEF.`,
				);
			}
		} else if (result.protocol === 'TLS' || result.protocol === 'DTLS') {
			result.firstByteOk = firstByte === 0x16;
			if (!result.firstByteOk) {
				result.errors.push(
					`Первый байт 0x${firstByte.toString(16)} — для ${result.protocol} ожидается 0x16.`,
				);
			}
		} else if (result.protocol === 'SIP') {
			result.firstByteOk =
				(firstByte >= 0x41 && firstByte <= 0x5a) || (firstByte >= 0x61 && firstByte <= 0x7a);
		}
	}

	return result;
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
