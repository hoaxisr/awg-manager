import type { SingboxRouterRule, SingboxRouterRuleSet } from '$lib/types';

export interface InlineRuleParseResult {
	rules: Record<string, unknown>[];
	warnings: string[];
	errors: string[];
}

export interface InlineLossyAnalysis {
	lossy: boolean;
	issues: string[];
}

function asStringArray(v: unknown): string[] {
	if (!Array.isArray(v)) return [];
	return v.filter((x): x is string => typeof x === 'string');
}

function asNumberArray(v: unknown): number[] {
	if (!Array.isArray(v)) return [];
	return v.filter((x): x is number => typeof x === 'number');
}

/** Host part without `*.` / leading `.` (for comparisons). */
function hostBase(s: string): string {
	return s.trim().replace(/^\*\./, '').replace(/^\./, '');
}

/** Bare IPv4/IPv6 → canonical CIDR for JSON (`/32` / `/128`). */
export function normalizeIpCidrForJson(s: string): string {
	if (isValidSimpleIp(s)) return `${s}/32`;
	if (isValidSimpleIpv6(s)) return `${s}/128`;
	return s;
}

/** Single-host CIDR → bare IP for smart-list display. */
export function formatIpCidrForList(cidr: string): string {
	const slash = cidr.lastIndexOf('/');
	if (slash < 0) return cidr;
	const ip = cidr.slice(0, slash);
	const mask = cidr.slice(slash + 1);
	if (mask === '32' && isValidSimpleIp(ip)) return ip;
	if (mask === '128' && isValidSimpleIpv6(ip)) return ip;
	return cidr;
}

function addIpCidr(set: Set<string>, raw: string): void {
	if (isValidIpCidr(raw)) {
		set.add(normalizeIpCidrForJson(raw));
	} else if (isValidSimpleIp(raw) || isValidSimpleIpv6(raw)) {
		set.add(normalizeIpCidrForJson(raw));
	}
}

/**
 * JSON rules → smart-list text.
 * - `domain` only → `domain:host`
 * - `domain_suffix` without dot → bare `host` (subtree / wildcard-style)
 * - `domain_suffix` with dot → `.host` line
 * - `domain` + matching no-dot suffix for same host → single bare `host`
 */
export function stringifyInlineRuleList(rules: Record<string, unknown>[] | undefined): string {
	if (!Array.isArray(rules) || rules.length === 0) return '';
	const lines: string[] = [];

	for (const r of rules) {
		const domains = asStringArray(r['domain']);
		const suffixes = asStringArray(r['domain_suffix']);
		const suffixNoDotSet = new Set(suffixes.filter((s) => !s.startsWith('.')).map(hostBase));

		for (const d of domains) {
			if (suffixNoDotSet.has(hostBase(d))) lines.push(d);
			else lines.push(`domain:${d}`);
		}
		for (const s of suffixes) {
			if (!s.startsWith('.') && domains.some((d) => hostBase(d) === hostBase(s))) continue;
			lines.push(s);
		}

		const keywords = asStringArray(r['domain_keyword']);
		for (const k of keywords) lines.push(`keyword:${k}`);

		const regexes = asStringArray(r['domain_regex']);
		for (const rx of regexes) lines.push(`regex:${rx}`);

		const ipCidrs = asStringArray(r['ip_cidr']);
		for (const ip of ipCidrs) lines.push(formatIpCidrForList(ip));

		const srcIpCidrs = asStringArray(r['source_ip_cidr']);
		for (const ip of srcIpCidrs) lines.push(`src_ip:${formatIpCidrForList(ip)}`);

		const ports = asNumberArray(r['port']);
		if (ports.length > 0) lines.push(`port:${ports.join(',')}`);

		const processNames = asStringArray(r['process_name']);
		for (const p of processNames) lines.push(`process:${p}`);

		const processPaths = asStringArray(r['process_path']);
		for (const p of processPaths) lines.push(`process_path:${p}`);

		const packageNames = asStringArray(r['package_name']);
		for (const p of packageNames) lines.push(`package:${p}`);

		const networks = asStringArray(r['network']);
		for (const n of networks) lines.push(`network:${n}`);
	}

	return lines.join('\n');
}

const SUPPORTED_INLINE_KEYS = new Set([
	'domain',
	'domain_suffix',
	'domain_keyword',
	'domain_regex',
	'ip_cidr',
	'source_ip_cidr',
	'port',
	'process_name',
	'process_path',
	'package_name',
	'network',
]);

const KEY_BUCKET: Record<string, string> = {
	domain: 'domain',
	domain_suffix: 'domain',
	domain_keyword: 'domain',
	domain_regex: 'domain',
	ip_cidr: 'ip_cidr',
	source_ip_cidr: 'source_ip_cidr',
	port: 'port',
	process_name: 'process_name',
	process_path: 'process_path',
	package_name: 'package_name',
	network: 'network',
};

export function analyzeInlineRuleListLossy(rules: Record<string, unknown>[] | undefined): InlineLossyAnalysis {
	if (!Array.isArray(rules) || rules.length === 0) return { lossy: false, issues: [] };

	const issues = new Set<string>();

	for (const rule of rules) {
		const supportedKeysInRule: string[] = [];

		for (const [key, value] of Object.entries(rule)) {
			if (!SUPPORTED_INLINE_KEYS.has(key)) {
				issues.add(`unsupported key: ${key}`);
				continue;
			}
			supportedKeysInRule.push(key);

			if (key === 'port') {
				if (!Array.isArray(value) || value.some((v) => typeof v !== 'number')) {
					issues.add(`invalid type for key: ${key}`);
				}
				continue;
			}

			if (!Array.isArray(value) || value.some((v) => typeof v !== 'string')) {
				issues.add(`invalid type for key: ${key}`);
			}
		}

		const buckets = new Set<string>();
		for (const key of supportedKeysInRule) {
			const bucket = KEY_BUCKET[key];
			if (bucket) buckets.add(bucket);
		}
		if (buckets.size > 1) {
			issues.add(
				`mixed supported keys in one rule are not round-trip safe: ${supportedKeysInRule.join(', ')}`,
			);
		}
	}

	return { lossy: issues.size > 0, issues: [...issues] };
}

type LogEntry = { line: number; msg: string };

function mkLogger(lineNum: number, w: LogEntry[], e: LogEntry[]) {
	return {
		warn(msg: string) { w.push({ line: lineNum, msg }); },
		err(msg: string) { e.push({ line: lineNum, msg }); },
	};
}

function real(val: string | undefined): boolean {
	return !!val && val !== '';
}

/** Strip trailing inline comments: space-semicolon or space-hash */
function stripInlineLine(s: string): string {
	return s
		.replace(/\s+;\s.*$/, '')
		.replace(/\s+#\s.*$/, '')
		.trim();
}

/** True when input has no non-comment, non-empty lines. */
export function isInlineRuleListEmpty(input: string): boolean {
	for (const rawLine of input.split(/\r?\n/)) {
		const line = stripInlineLine(rawLine);
		if (line === '' || line.startsWith(';') || line.startsWith('#') || line.startsWith('//')) {
			continue;
		}
		return false;
	}
	return true;
}

export function parseInlineRuleList(input: string): InlineRuleParseResult {
	const warnings: LogEntry[] = [];
	const errors: LogEntry[] = [];

	const domainGroup: Set<string> = new Set();
	const domainSuffixGroup: Set<string> = new Set();
	const domainKeywordGroup: Set<string> = new Set();
	const domainRegexGroup: Set<string> = new Set();
	const ipCidrGroup: Set<string> = new Set();
	const sourceIpCidrGroup: Set<string> = new Set();
	const portGroup: Set<number> = new Set();
	const processNameGroup: Set<string> = new Set();
	const processPathGroup: Set<string> = new Set();
	const packageNameGroup: Set<string> = new Set();
	const networkGroup: Set<string> = new Set();

	const lines = input.split(/\r?\n/);

	for (let i = 0; i < lines.length; i++) {
		let line = stripInlineLine(lines[i]);
		const lineNum = i + 1;

		// skip empty and full-line comments
		if (line === '' || line.startsWith(';') || line.startsWith('#') || line.startsWith('//')) continue;

		const lg = mkLogger(lineNum, warnings, errors);

		// ── adblock exceptions ───────────────────────────────────
		if (line.startsWith('@@')) {
			lg.warn('исключения @@ пока не поддерживаются в inline rule set');
			continue;
		}

		// ── strip adblock prefix noise ───────────────────────────
		if (line.startsWith('||') && line.endsWith('^')) {
			line = line.slice(2, line.length - 1).trim();
		} else if (line.startsWith('||')) {
			line = line.slice(2).trim();
		} else if (line.endsWith('^')) {
			line = line.slice(0, line.length - 1).trim();
		}

		// ── parse key:value prefixes ─────────────────────────────
		const colonIdx = line.indexOf(':');
		if (colonIdx > 0) {
			const rawKey = line.slice(0, colonIdx);
			const val = line.slice(colonIdx + 1).trim();

			switch (rawKey.toLowerCase()) {
				case 'port':
					if (!real(val)) { lg.err('port требует значение после двоеточия'); continue; }
					let portAdded = false;
					for (const p of val.split(',').map((s) => s.trim()).filter(real)) {
						if (!/^\d+$/.test(p)) { lg.err(`Некорректный порт: ${p}`); continue; }
						const n = parseInt(p, 10);
						if (n < 1 || n > 65535) { lg.err(`Некорректный порт: ${p}`); continue; }
						portGroup.add(n);
						portAdded = true;
					}
					if (portAdded) { lg.warn('port создаёт отдельное правило и может матчить широкий трафик'); }
					continue;

				case 'port_range':
					if (!real(val)) { lg.err(`${rawKey} требует значение после двоеточия`); continue; }
					lg.warn('port_range пока не поддерживается в smart list');
					continue;

				case 'process':
				case 'process_name':
					if (!real(val)) { lg.err(`${rawKey} требует значение после двоеточия`); continue; }
					processNameGroup.add(val);
					lg.warn('process относится к локальным процессам роутера/хоста, а не к LAN-клиентам');
					continue;

				case 'process_path':
				case 'path':
					if (!real(val)) { lg.err(`${rawKey} требует значение после двоеточия`); continue; }
					processPathGroup.add(val);
					lg.warn('process_path относится к локальным процессам роутера/хоста, а не к LAN-клиентам');
					continue;

				case 'package':
				case 'package_name':
					if (!real(val)) { lg.err(`${rawKey} требует значение после двоеточия`); continue; }
					packageNameGroup.add(val);
					continue;

				case 'network':
					if (!real(val)) { lg.err(`${rawKey} требует значение после двоеточия`); continue; }
					const network = val.toLowerCase();
					if (network !== 'tcp' && network !== 'udp') {
						lg.err(`Некорректный network: ${val} — допустимы только tcp/udp`);
						continue;
					}
					networkGroup.add(network);
					continue;

				case 'ip':
					if (!real(val)) { lg.err(`${rawKey} требует значение после двоеточия`); continue; }
					for (const p of val.split(',').map((s) => s.trim()).filter(real)) {
						if (isValidIpCidr(p) || isValidSimpleIp(p) || isValidSimpleIpv6(p)) {
							addIpCidr(ipCidrGroup, p);
						} else {
							lg.err(`Некорректный IP/CIDR для ip: ${p}`);
						}
					}
					continue;

				case 'cidr':
					if (!real(val)) { lg.err(`${rawKey} требует значение после двоеточия`); continue; }
					for (const p of val.split(',').map((s) => s.trim()).filter(real)) {
						if (isValidIpCidr(p) || isValidSimpleIp(p) || isValidSimpleIpv6(p)) {
							addIpCidr(ipCidrGroup, p);
						} else {
							lg.err(`Некорректный CIDR для cidr: ${p}`);
						}
					}
					continue;

				case 'src_ip':
				case 'source_ip':
					if (!real(val)) { lg.err(`${rawKey} требует значение после двоеточия`); continue; }
					for (const p of val.split(',').map((s) => s.trim()).filter(real)) {
						if (isValidIpCidr(p) || isValidSimpleIp(p) || isValidSimpleIpv6(p)) {
							addIpCidr(sourceIpCidrGroup, p);
						} else {
							lg.err(`Некорректный IP/CIDR для src_ip: ${p}`);
						}
					}
					continue;

				case 'domain_keyword':
				case 'keyword':
					if (!real(val)) { lg.err(`${rawKey} требует значение после двоеточия`); continue; }
					domainKeywordGroup.add(val);
					continue;

				case 'domain_regex':
				case 'regex':
					if (!real(val)) { lg.err(`${rawKey} требует значение после двоеточия`); continue; }
					try { new RegExp(val); } catch { lg.err(`Некорректный regex: ${val}`); continue; }
					domainRegexGroup.add(val);
					continue;

				case 'domain':
					if (!real(val)) { lg.err(`${rawKey} требует значение после двоеточия`); continue; }
					{
						const norm = normalizeDomainHost(val);
						if (norm) {
							domainGroup.add(norm);
						} else {
							lg.err(`Некорректный домен для domain: ${val}`);
						}
					}
					continue;

				case 'domain_suffix':
				case 'suffix':
					if (!real(val)) { lg.err(`${rawKey} требует значение после двоеточия`); continue; }

					if (val.startsWith('*.')) {
						const norm = normalizeDomainHost(val.slice(2));
						if (norm) {
							domainSuffixGroup.add(norm);
						} else {
							lg.err(`Некорректный домен для ${rawKey}: ${val}`);
						}
					} else if (val.startsWith('.')) {
						const norm = normalizeDomainHost(val.slice(1));
						if (norm) {
							domainSuffixGroup.add('.' + norm);
						} else {
							lg.err(`Некорректный домен для ${rawKey}: ${val}`);
						}
					} else {
						const norm = normalizeDomainHost(val);
						if (norm) {
							domainSuffixGroup.add(norm);
						} else {
							lg.err(`Некорректный домен для ${rawKey}: ${val}`);
						}
					}
					continue;

				default:
					break;
			}
		}

		// ── domain / hostname / bare IP ──────────────────────────
		if (line === '') continue;

		let domain = line;

		// URL → hostname
		if (/^https?:\/\//i.test(domain)) {
			try { domain = new URL(domain).hostname; } catch (_) { /* keep as-is */ }
		}

		// ".example.com" → domain_suffix [".example.com"]
		if (domain.startsWith('.')) {
			const norm = normalizeDomainHost(domain.slice(1));
			if (norm) {
				domainSuffixGroup.add('.' + norm);
			} else {
				lg.warn(`Невалидный домен (точка-префикс): ${domain}`);
			}
			continue;
		}

		// "*.example.com" → domain_suffix ["example.com"] (без точки)
		if (domain.startsWith('*.')) {
			const norm = normalizeDomainHost(domain.slice(2));
			if (norm) {
				domainSuffixGroup.add(norm);
			} else {
				lg.warn(`Невалидный домен (wildcard): ${domain}`);
			}
			continue;
		}

		// CIDR or bare IP → ip_cidr with /32 or /128 for single hosts
		if (isValidIpCidr(domain) || isValidSimpleIp(domain) || isValidSimpleIpv6(domain)) {
			addIpCidr(ipCidrGroup, domain);
			continue;
		}

		// pure-IP strings (e.g. "999.999.999.999") that didn't match the canonical
		// isValidSimpleIp but match DOMAIN_RE by coincidence — drain here
		if (/^\d+\.\d+\.\d+\.\d+$/.test(domain)) {
			lg.warn(`Не распознано как правило: ${line}`);
			continue;
		}

		// plain valid domain → только domain_suffix без точки (поддерево), не domain
		{
			const norm = normalizeDomainHost(domain);
			if (norm) {
				domainSuffixGroup.add(norm);
				continue;
			}
		}

		lg.warn(`Не распознано как правило: ${line}`);
	}

	// ── build rules array ────────────────────────────────────────
	const rules: Record<string, unknown>[] = [];

	if (domainGroup.size > 0 || domainSuffixGroup.size > 0 || domainKeywordGroup.size > 0 || domainRegexGroup.size > 0) {
		const g: Record<string, unknown> = {};
		if (domainKeywordGroup.size > 0) g['domain_keyword'] = [...domainKeywordGroup];
		if (domainSuffixGroup.size > 0) g['domain_suffix'] = [...domainSuffixGroup];
		if (domainGroup.size > 0) g['domain'] = [...domainGroup];
		if (domainRegexGroup.size > 0) g['domain_regex'] = [...domainRegexGroup];
		rules.push(g);
	}
	if (ipCidrGroup.size > 0) rules.push({ ip_cidr: [...ipCidrGroup] });
	if (sourceIpCidrGroup.size > 0) rules.push({ source_ip_cidr: [...sourceIpCidrGroup] });
	if (portGroup.size > 0) rules.push({ port: [...portGroup].sort((a, b) => a - b) });
	if (processNameGroup.size > 0) rules.push({ process_name: [...processNameGroup] });
	if (processPathGroup.size > 0) rules.push({ process_path: [...processPathGroup] });
	if (packageNameGroup.size > 0) rules.push({ package_name: [...packageNameGroup] });
	if (networkGroup.size > 0) rules.push({ network: [...networkGroup] });

	// ── format for return ───────────────────────────────────────
	const warnMsgs = warnings.map((w) => `Строка ${w.line}: ${w.msg}`);
	const errMsgs = errors.map((e) => `Строка ${e.line}: ${e.msg}`);

	if (rules.length === 0 && errMsgs.length === 0 && warnMsgs.length === 0 && !isInlineRuleListEmpty(input)) {
		errMsgs.push('Нет валидных строк для inline rule set');
	}

	return { rules, warnings: warnMsgs, errors: errMsgs };
}

// ─────────────────────────────────────────────────────────────
// Validation helpers
// ─────────────────────────────────────────────────────────────

const DOMAIN_RE = /^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)*$/i;

function isAsciiDomainHost(s: string): boolean {
	return DOMAIN_RE.test(s);
}

/** Unicode/IDN hostname → punycode (sing-box expects ASCII). */
export function toAsciiHostname(host: string): string | null {
	const trimmed = host.trim();
	if (!trimmed || /[\s/:#]/.test(trimmed)) return null;
	try {
		const ascii = new URL(`http://${trimmed}`).hostname;
		if (!ascii || !isAsciiDomainHost(ascii)) return null;
		return ascii;
	} catch {
		return null;
	}
}

/** Normalize host (strip `*.`/`.`) and convert to punycode when needed. */
function normalizeDomainHost(host: string): string | null {
	return toAsciiHostname(hostBase(host));
}

function isValidIpCidr(s: string): boolean {
	const parts = s.split('/');
	if (parts.length !== 2) return false;

	const [ip, mask] = parts;
	if (!ip || !mask) return false;
	if (!/^\d+$/.test(mask)) return false;

	const maskNum = Number(mask);

	if (isValidSimpleIp(ip)) {
		return maskNum >= 0 && maskNum <= 32;
	}

	if (isValidSimpleIpv6(ip)) {
		return maskNum >= 0 && maskNum <= 128;
	}

	return false;
}

function isValidSimpleIp(s: string): boolean {
	const parts = s.split('.');
	if (parts.length !== 4) return false;
	return parts.every((p) => /^\d{1,3}$/.test(p) && parseInt(p, 10) <= 255);
}

function isValidSimpleIpv6(addr: string): boolean {
	if (!addr.includes(':')) return false;
	// Allow hex chars, ':', '.' (IPv4-mapped suffix)
	if (/[^0-9a-f:.]/i.test(addr)) return false;
	const hextets = addr.split(':');
	if (hextets.length < 3 || hextets.length > 8) return false;
	return hextets.every((h) => h === '' || /^[0-9a-f]{0,4}$/i.test(h));
}

/** Reserved for AWG-compiled inline → local .srs companion (see ruleset_materializer). */
export const INLINE_RULE_SET_SRS_SUFFIX = '-srs';
/** Имя файла артефакта для inline-набора, чей тег санитайзинг схлопнул в пустоту
 *  (зеркало fallbackRuleSetFilename на бэкенде, F434 #941). */
const FALLBACK_RULE_SET_FILENAME = 'ruleset';

/** Strip compiled companion suffix; mirrors backend rewriteSRSSuffixRuleSetRefs. */
export function inlineTagFromSRSTag(tag: string): string | null {
	if (!tag.endsWith(INLINE_RULE_SET_SRS_SUFFIX)) return null;
	const base = tag.slice(0, -INLINE_RULE_SET_SRS_SUFFIX.length);
	return base || null;
}

/** UI label — never show the reserved -srs companion suffix. */
export function displayRuleSetTag(tag: string): string {
	return inlineTagFromSRSTag(tag) ?? tag;
}

/** Resolve inline base tag when rule_set[] briefly references the compiled companion. */
export function resolveRuleSetByTag(
	tag: string,
	ruleSets: { tag: string; type?: string }[],
): { tag: string; type?: string } | undefined {
	const byTag = new Map(ruleSets.filter((rs) => rs.tag).map((rs) => [rs.tag, rs] as const));
	const direct = byTag.get(tag);
	const base = inlineTagFromSRSTag(tag);

	if (base) {
		const inline = byTag.get(base);
		if (inline?.type === 'inline') return inline;
	}

	if (direct) return direct;
	if (base) return byTag.get(base);
	return undefined;
}

export function isCompiledSRSCompanion(rs: { tag: string; type?: string }): boolean {
	return rs.type === 'local' && inlineTagFromSRSTag(rs.tag) !== null;
}

/** Strip -srs refs from route rules (defense in depth for SSE / staging races). */
export function normalizeRuleForUI(rule: SingboxRouterRule): SingboxRouterRule {
	const tags = rule.rule_set;
	if (!tags?.length) return rule;
	const next = tags.map(displayRuleSetTag);
	if (next.every((t, i) => t === tags[i])) return rule;
	return { ...rule, rule_set: next };
}

export function normalizeRulesForUI(rules: SingboxRouterRule[]): SingboxRouterRule[] {
	return rules.map(normalizeRuleForUI);
}

/** Hide compiled .srs companions when the inline base is already listed. */
export function normalizeRuleSetsForUI(ruleSets: SingboxRouterRuleSet[]): SingboxRouterRuleSet[] {
	const inlineTags = new Set(
		ruleSets.filter((rs) => rs.type === 'inline' && rs.tag).map((rs) => rs.tag),
	);
	return ruleSets.filter((rs) => {
		if (!isCompiledSRSCompanion(rs)) return true;
		const base = inlineTagFromSRSTag(rs.tag);
		return !(base && inlineTags.has(base));
	});
}

/** JSON rules → smart-list для визарда / редактора простых правил. */
export function stringifyInlineRuleListForWizard(
	rules: Record<string, unknown>[] | undefined,
): string {
	return stringifyInlineRuleList(rules);
}

const WIZARD_STRIP_INLINE_KEYS = [
	'source_ip_cidr',
	'port',
	'process_name',
	'process_path',
	'package_name',
	'network',
] as const;

/** Убирает поля, которые в простом режиме задаются на rule, не в списке. */
export function stripWizardRuleOnlyFieldsFromInlineRules(
	rules: Record<string, unknown>[],
): Record<string, unknown>[] {
	return rules
		.map((r) => {
			const next = { ...r };
			for (const k of WIZARD_STRIP_INLINE_KEYS) delete next[k];
			return next;
		})
		.filter((r) => Object.keys(r).length > 0);
}

export function collectSourceIpCidrFromInlineRules(rules: Record<string, unknown>[]): boolean {
	return rules.some((r) => asStringArray(r['source_ip_cidr']).length > 0);
}

export function collectPortFromInlineRules(rules: Record<string, unknown>[]): boolean {
	return rules.some((r) => asNumberArray(r['port']).length > 0);
}

/** Returns a user-facing error, or null when the tag is allowed.
 *  `type` обязателен: правило про имя файла касается только inline-наборов
 *  (F434, #941), и необязательный параметр молча пропускал бы проверку. */
export function validateRuleSetTag(
	tag: string,
	type: SingboxRouterRuleSet['type'],
): string | null {
	const t = tag.trim();
	if (!t) return 'Tag обязателен';
	if (t.endsWith(INLINE_RULE_SET_SRS_SUFFIX)) {
		return `Суффикс «${INLINE_RULE_SET_SRS_SUFFIX}» зарезервирован для скомпилированного набора — укажите имя без него (например geosite-samsung, не geosite-samsung-srs)`;
	}
	// F434 (#941): зеркало validateRuleSet на бэкенде. Файл артефакта набора
	// назван самим тегом, пропущенным через лоссовый санитайзинг, поэтому
	// «Моё» и «Второй» (как и «-foo» с «foo») схлопывались в один файл и
	// затирали правила друг друга. Годен только тег, равный своему имени файла.
	// Только inline: у remote тег файл не именует, а теги каталога SagerNet
	// со спецсимволами (geolocation-!cn) штатные.
	if (type === 'inline' && t === FALLBACK_RULE_SET_FILENAME) {
		return `Имя «${FALLBACK_RULE_SET_FILENAME}» занято: так называется файл наборов, заведённых с кириллическим именем до запрета — набор с этим именем разделил бы файл с ними`;
	}
	if (type === 'inline' && t.replace(/[^A-Za-z0-9._-]+/g, '-').replace(/^-+|-+$/g, '') !== t) {
		return 'Имя набора — это имя его файла: латиница, цифры, точка, подчёркивание и дефис (дефис не с краю). Кириллица и пробелы не годятся: два таких имени дают один файл и затирают друг друга';
	}
	return null;
}
