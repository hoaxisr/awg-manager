import { describe, it, expect } from 'vitest';
import {
	analyzeInlineRuleListLossy,
	displayRuleSetTag,
	isInlineRuleListEmpty,
	normalizeRuleForUI,
	normalizeRuleSetsForUI,
	parseInlineRuleList,
	resolveRuleSetByTag,
	stringifyInlineRuleList,
	toAsciiHostname,
	validateRuleSetTag,
} from '$lib/utils/singboxInlineRules';

describe('toAsciiHostname', () => {
	it('converts IDN labels to punycode', () => {
		expect(toAsciiHostname('рф')).toBe('xn--p1ai');
		expect(toAsciiHostname('пример.рф')).toBe(new URL('http://пример.рф').hostname);
	});

	it('leaves ASCII hostnames unchanged (lowercased)', () => {
		expect(toAsciiHostname('Example.COM')).toBe('example.com');
		expect(toAsciiHostname('xn--p1ai')).toBe('xn--p1ai');
	});
});

describe('parseInlineRuleList', () => {
	// ── domains ────────────────────────────────────────────────
	it('maps bare domain to domain_suffix without dot (not domain)', () => {
		const input = [
			'openai.com',
			'*.perplexity.ai',
			'https://gemini.google.com/app',
		].join('\n');
		const { rules, errors, warnings } = parseInlineRuleList(input);
		expect(errors).toHaveLength(0);
		expect(warnings).toHaveLength(0);
		expect(rules).toHaveLength(1);
		const g = rules[0];
		expect(g['domain']).toBeUndefined();
		expect(g['domain_suffix'] as string[]).toContain('openai.com');
		expect(g['domain_suffix'] as string[]).toContain('gemini.google.com');
		expect(g['domain_suffix'] as string[]).toContain('perplexity.ai');
	});

	it('maps dot-prefix line to domain_suffix with dot', () => {
		const { rules, errors } = parseInlineRuleList('.example.com');
		expect(errors).toHaveLength(0);
		expect(rules).toEqual([{ domain_suffix: ['.example.com'] }]);
	});

	it('converts unicode domains to punycode in domain_suffix', () => {
		const zone = toAsciiHostname('рф')!;
		const host = toAsciiHostname('пример.рф')!;
		expect(parseInlineRuleList('пример.рф').rules).toEqual([{ domain_suffix: [host] }]);
		expect(parseInlineRuleList('*.рф').rules).toEqual([{ domain_suffix: [zone] }]);
		expect(parseInlineRuleList('.рф').rules).toEqual([{ domain_suffix: ['.' + zone] }]);
		expect(parseInlineRuleList(`domain:пример.рф`).rules).toEqual([{ domain: [host] }]);
	});

	// ── adblock ────────────────────────────────────────────────
	it('parses ||claude.ai^ as suffix-only', () => {
		const { rules, errors, warnings } = parseInlineRuleList('||claude.ai^');
		expect(errors).toHaveLength(0);
		expect(warnings).toHaveLength(0);
		expect(rules).toEqual([{ domain_suffix: ['claude.ai'] }]);
	});

	it('warns on @@ exception and skips it', () => {
		const { rules, errors, warnings } = parseInlineRuleList('@@||example.com^');
		expect(rules).toHaveLength(0);
		expect(errors).toHaveLength(0);
		expect(warnings).toHaveLength(1);
		expect(warnings[0]).toBe('Строка 1: исключения @@ пока не поддерживаются в inline rule set');
	});

	// ── IP/CIDR ───────────────────────────────────────────────
	it('creates separate ip_cidr group with /32 for bare IPv4', () => {
		const input = ['1.1.1.1', '8.8.8.0/24'].join('\n');
		const { rules, errors } = parseInlineRuleList(input);
		expect(errors).toHaveLength(0);
		expect(rules).toEqual([{ ip_cidr: ['1.1.1.1/32', '8.8.8.0/24'] }]);
	});

	// ── prefix fields ─────────────────────────────────────────
	it('creates separate groups for each prefix field', () => {
		const r = parseInlineRuleList([
			'keyword:youtube',
			'regex:^(.+\\.)?example-cdn\\.',
			'port:443,8443',
			'process:curl',
			'package:com.termux',
			'network:tcp',
		].join('\n'));
		expect(r.errors).toHaveLength(0);
		expect(r.rules).toHaveLength(5);

		expect(r.rules[0]).toEqual({ domain_keyword: ['youtube'], domain_regex: ['^(.+\\.)?example-cdn\\.'] });
		expect(r.rules[1]).toEqual({ port: [443, 8443] });
		expect(r.rules[2]).toEqual({ process_name: ['curl'] });
		expect(r.rules[3]).toEqual({ package_name: ['com.termux'] });
		expect(r.rules[4]).toEqual({ network: ['tcp'] });
	});

	// ── unsupported network ───────────────────────────────────
	it('rejects unsupported network value as error, does not add to group', () => {
		const { rules, errors, warnings } = parseInlineRuleList('network:http');
		expect(errors).toHaveLength(1);
		expect(warnings).toHaveLength(0);
		expect(errors[0]).toContain('http');
		expect(rules).toHaveLength(0);
	});

	// ── invalid port ──────────────────────────────────────────
	it('reports error on invalid port boundary value', () => {
		const { errors } = parseInlineRuleList('port:99999');
		expect(errors).toHaveLength(1);
		expect(errors[0]).toContain('99999');
	});

	it('rejects non-numeric port token', () => {
		const { rules, errors, warnings } = parseInlineRuleList('port:443abc');
		expect(errors).toHaveLength(1);
		expect(errors[0]).toContain('443abc');
		expect(rules).toHaveLength(0);
		expect(warnings).toHaveLength(0);
	});

	it('rejects port range token', () => {
		const { rules, errors, warnings } = parseInlineRuleList('port:80-90');
		expect(errors).toHaveLength(1);
		expect(errors[0]).toContain('80-90');
		expect(rules).toHaveLength(0);
		expect(warnings).toHaveLength(0);
	});

	// ── empty input ───────────────────────────────────────────
	it('returns no errors on empty input (empty is not a parse error)', () => {
		const { rules, errors } = parseInlineRuleList('');
		expect(rules).toHaveLength(0);
		expect(errors).toHaveLength(0);
		expect(isInlineRuleListEmpty('')).toBe(true);
	});

	it('treats comment-only input as empty (no parse error)', () => {
		const input = ['# comment', '// line', '; semi'].join('\n');
		const { rules, errors } = parseInlineRuleList(input);
		expect(rules).toHaveLength(0);
		expect(errors).toHaveLength(0);
		expect(isInlineRuleListEmpty(input)).toBe(true);
	});

	// ── comments ──────────────────────────────────────────────
	it('skips #, // and ; comments silently', () => {
		const input = [
			'# comment',
			'// line comment',
			'; semicolon comment',
			'openai.com',
		].join('\n');
		const r = parseInlineRuleList(input);
		expect(r.errors).toHaveLength(0);
		expect(r.rules).toHaveLength(1);
		expect(r.warnings).toHaveLength(0);
	});

	// ── inline comments ───────────────────────────────────────
	it('strips inline trailing comments', () => {
		const { rules } = parseInlineRuleList('openai.com # this is a comment');
		expect(rules).toEqual([{ domain_suffix: ['openai.com'] }]);
	});

	// ── src_ip / source_ip ────────────────────────────────────
	it('validates src_ip and source_ip values; invalid IP produces error', () => {
		const input = ['src_ip:192.168.1.50', 'source_ip:not-an-ip', 'source_ip:10.0.0.1/24'].join('\n');
		const { rules, errors } = parseInlineRuleList(input);
		expect(errors).toHaveLength(1);
		expect(errors[0]).toContain('src_ip');
		expect(rules).toEqual([{ source_ip_cidr: ['192.168.1.50/32', '10.0.0.1/24'] }]);
	});

	// ── dedup ─────────────────────────────────────────────────
	it('deduplicates values within each group', () => {
		const input = ['openai.com', 'openai.com', 'port:443,443'].join('\n');
		const { rules } = parseInlineRuleList(input);
		const g = rules.find((r) => (r).domain_suffix);
		expect((g!['domain_suffix'] as string[])).toEqual(['openai.com']);
	});

	// ── domain:/suffix:/domain_suffix: prefixes ───────────────
	it('handles domain: prefix as domain-only (no domain_suffix)', () => {
		const { rules } = parseInlineRuleList('domain:example.com');
		expect(rules).toEqual([{ domain: ['example.com'] }]);
	});

	it('handles domain_suffix: prefix with leading dot in JSON', () => {
		const { rules } = parseInlineRuleList('domain_suffix:.example.com');
		expect(rules).toEqual([{ domain_suffix: ['.example.com'] }]);
	});

	it('handles domain_suffix: bare host as no-dot suffix', () => {
		const { rules } = parseInlineRuleList('domain_suffix:example.com');
		expect(rules).toEqual([{ domain_suffix: ['example.com'] }]);
	});

	it('keeps explicit dotted domain_suffix as dotted suffix', () => {
		const { rules } = parseInlineRuleList('domain_suffix:.example.com');
		expect(rules).toEqual([{ domain_suffix: ['.example.com'] }]);
	});

	it('handles suffix: wildcard as no-dot suffix', () => {
		const { rules } = parseInlineRuleList('suffix:*.example.org');
		expect(rules).toEqual([{ domain_suffix: ['example.org'] }]);
	});

	it('handles domain_keyword: and domain_regex: prefixes', () => {
		const { rules } = parseInlineRuleList(['domain_keyword:youtube', 'domain_regex:^ad\\.'].join('\n'));
		expect(rules).toEqual([{ domain_keyword: ['youtube'], domain_regex: ['^ad\\.'] }]);
	});

	// ── IP validation ────────────────────────────────────────
	it('rejects CIDR with out-of-range mask (999)', () => {
		const { rules, errors } = parseInlineRuleList('cidr:8.8.8.0/999');
		expect(rules).toHaveLength(0);
		expect(errors).toHaveLength(1);
	});

	it('accepts valid bare IPv4 and CIDR', () => {
		const { rules, errors } = parseInlineRuleList('1.2.3.4\n5.6.7.8/16');
		expect(errors).toHaveLength(0);
		expect(rules).toEqual([{ ip_cidr: ['1.2.3.4/32', '5.6.7.8/16'] }]);
	});

	// ── domain: invalid value ────────────────────────────────
	it('rejects invalid domain value for domain: prefix', () => {
		const { rules, errors } = parseInlineRuleList('domain:!!!invalid!!!');
		expect(rules).toHaveLength(0);
		expect(errors).toHaveLength(1);
	});

	it('port token keeps valid entries after garbage token', () => {
		const r = parseInlineRuleList('port:443,443abc,8443');
		expect(r.errors).toHaveLength(1);
		expect(r.errors[0]).toContain('443abc');
		expect(r.warnings).toHaveLength(1);
		expect(r.warnings[0]).toContain('широкий трафик');
	});

	// ── wide-matcher warnings ──────────────────────────────────
	it('emits single wide-traffic warning for single port', () => {
		const { rules, errors, warnings } = parseInlineRuleList('port:443');
		expect(errors).toHaveLength(0);
		expect(rules).toEqual([{ port: [443] }]);
		expect(warnings).toHaveLength(1);
		expect(warnings[0]).toContain('широкий трафик');
	});

	it('emits single wide-traffic warning for multi-port list', () => {
		const { rules, warnings } = parseInlineRuleList('port:443,8443');
		expect(warnings).toHaveLength(1);
		expect(warnings[0]).toContain('широкий трафик');
		expect(rules[0]).toEqual({ port: [443, 8443] });
	});

	it('emits process local-process warning', () => {
		const { rules, errors, warnings } = parseInlineRuleList('process:curl');
		expect(errors).toHaveLength(0);
		expect(rules).toEqual([{ process_name: ['curl'] }]);
		expect(warnings).toHaveLength(1);
		expect(warnings[0]).toContain('локальным процессам');
	});

	it('emits process_path local-process warning', () => {
		const { rules, errors, warnings } = parseInlineRuleList('process_path:/usr/bin/curl');
		expect(errors).toHaveLength(0);
		expect(rules).toEqual([{ process_path: ['/usr/bin/curl'] }]);
		expect(warnings).toHaveLength(1);
		expect(warnings[0]).toContain('локальным процессам');
	});
});

// ── IPv6 ───────────────────────────────────────────────────────
describe('IPv6 support', () => {
	it('accepts bare IPv6 address as /128 in JSON', () => {
		const { rules, errors } = parseInlineRuleList('2606:4700::1');
		expect(errors).toHaveLength(0);
		expect(rules).toEqual([{ ip_cidr: ['2606:4700::1/128'] }]);
	});

	it('accepts IPv6 CIDR notation', () => {
		const { rules, errors } = parseInlineRuleList('2606:4700::/32');
		expect(errors).toHaveLength(0);
		expect(rules).toEqual([{ ip_cidr: ['2606:4700::/32'] }]);
	});

	it('accepts ip: prefix with IPv6 address', () => {
		const { rules, errors } = parseInlineRuleList('ip:2606:4700::1');
		expect(errors).toHaveLength(0);
		expect(rules).toEqual([{ ip_cidr: ['2606:4700::1/128'] }]);
	});

	it('rejects IPv6 CIDR with out-of-range mask (129)', () => {
		const { rules, errors } = parseInlineRuleList('cidr:2606:4700::/129');
		expect(rules).toHaveLength(0);
		expect(errors).toHaveLength(1);
		expect(errors[0]).toContain('cidr');
	});
});

// ── port_range ─────────────────────────────────────────────────
describe('port_range (not yet supported)', () => {
	it('warns that port_range is unsupported, does not generate rule', () => {
		const { rules, errors, warnings } = parseInlineRuleList('port_range:1000-2000');
		expect(rules).toHaveLength(0);
		expect(errors).toHaveLength(0);
		expect(warnings).toHaveLength(1);
		expect(warnings[0]).toContain('port_range');
		expect(warnings[0]).toContain('не поддерживается');
	});
});

describe('stringifyInlineRuleList', () => {
	it('serializes /32 and /128 single-host CIDR as bare IP', () => {
		expect(stringifyInlineRuleList([{ ip_cidr: ['1.1.1.1/32', '8.8.8.0/24'] }])).toBe(
			'1.1.1.1\n8.8.8.0/24',
		);
		expect(stringifyInlineRuleList([{ ip_cidr: ['2606:4700::1/128'] }])).toBe('2606:4700::1');
	});

	it('serializes domain-only JSON as domain: line', () => {
		const text = stringifyInlineRuleList([{ domain: ['example.com'] }]);
		expect(text).toBe('domain:example.com');
		expect(parseInlineRuleList(text).rules).toEqual([{ domain: ['example.com'] }]);
	});

	it('serializes no-dot suffix as bare host line', () => {
		const rules = [{ domain_suffix: ['x.com'] }];
		const text = stringifyInlineRuleList(rules);
		expect(text).toBe('x.com');
		expect(parseInlineRuleList(text).rules).toEqual(rules);
	});

	it('serializes dotted suffix as dot-prefixed line', () => {
		const text = stringifyInlineRuleList([{ domain_suffix: ['.example.com'] }]);
		expect(text).toBe('.example.com');
	});

	it('round-trips typical list input through parse → stringify → parse', () => {
		const input = ['openai.com', '*.perplexity.ai', '1.1.1.1', 'keyword:youtube'].join('\n');
		const p1 = parseInlineRuleList(input);
		expect(p1.errors).toHaveLength(0);
		const text = stringifyInlineRuleList(p1.rules);
		expect(text).toContain('1.1.1.1');
		expect(text).not.toContain('/32');
		const p2 = parseInlineRuleList(text);
		expect(p2.errors).toHaveLength(0);
		expect(p2.rules).toEqual(p1.rules);
	});

	it('keeps domain and broader suffix as separate lines', () => {
		const rules = [{ domain: ['www.example.com'], domain_suffix: ['example.com'] }];
		const text = stringifyInlineRuleList(rules);
		expect(text).toBe(['domain:www.example.com', 'example.com'].join('\n'));
		const p = parseInlineRuleList(text);
		expect(p.errors).toHaveLength(0);
		expect(p.rules[0]).toEqual(rules[0]);
	});
});

describe('analyzeInlineRuleListLossy', () => {
	it('does not flag domain-only rules as lossy', () => {
		const r = analyzeInlineRuleListLossy([{ domain: ['example.com'] }]);
		expect(r.lossy).toBe(false);
	});

	it('flags mixed domain and ip matchers in one rule object', () => {
		const r = analyzeInlineRuleListLossy([{ domain: ['a.com'], ip_cidr: ['1.1.1.1'] }]);
		expect(r.lossy).toBe(true);
		expect(r.issues.some((i) => i.includes('mixed supported keys'))).toBe(true);
	});

	it('flags unsupported JSON keys', () => {
		const r = analyzeInlineRuleListLossy([{ domain: ['z.com'], inverted: [true] }]);
		expect(r.lossy).toBe(true);
		expect(r.issues.some((i) => i.includes('inverted'))).toBe(true);
	});
});

describe('validateRuleSetTag', () => {
	it('rejects empty tag', () => {
		expect(validateRuleSetTag('', 'inline')).toMatch(/обязателен/i);
	});

	it('rejects reserved -srs suffix', () => {
		expect(validateRuleSetTag('geosite-samsung-srs', 'remote')).toMatch(/-srs/);
	});

	it('allows base tag', () => {
		expect(validateRuleSetTag('geosite-samsung', 'remote')).toBeNull();
	});

	// F434 (#941): тег = имя файла артефакта, иначе два набора делят один файл.
	it.each(['Моё', 'Второй', 'My set', '-foo', 'foo-', 'geosite/example'])(
		'rejects lossy inline tag %s',
		(tag) => {
			expect(validateRuleSetTag(tag, 'inline')).toMatch(/имя его файла/i);
		},
	);

	// Заглавная латиница годна — кейс сторожит расхождение с бэкендом, где
	// класс символов [A-Za-z0-9._-].
	it.each(['custom-1', 'geosite-telegram', 'a.b_c-1', 'MyCustomSet'])(
		'allows file-safe inline tag %s',
		(tag) => {
			expect(validateRuleSetTag(tag, 'inline')).toBeNull();
		},
	);

	// Литерал имени файла легаси-наборов занят — но только он сам.
	it('rejects the reserved fallback file name as an inline tag', () => {
		expect(validateRuleSetTag('ruleset', 'inline')).toMatch(/занято/i);
		expect(validateRuleSetTag('ruleset-1', 'inline')).toBeNull();
		expect(validateRuleSetTag('ruleset', 'remote')).toBeNull();
	});

	// Пробелы по краям срезаются до сравнения — иначе годный тег отбивался бы
	// на ровном месте.
	it('trims before comparing', () => {
		expect(validateRuleSetTag('  custom-1  ', 'inline')).toBeNull();
	});

	// У remote тег файл не именует: теги каталога SagerNet со спецсимволами
	// штатные (geolocation-!cn лежит и в наших пресетах).
	it.each(['geosite-geolocation-!cn', 'geosite-xiaomi@cn'])(
		'allows catalog tag %s for remote',
		(tag) => {
			expect(validateRuleSetTag(tag, 'remote')).toBeNull();
		},
	);
});

describe('displayRuleSetTag', () => {
	it('strips compiled companion suffix', () => {
		expect(displayRuleSetTag('geosite-samsung-srs')).toBe('geosite-samsung');
		expect(displayRuleSetTag('geosite-samsung')).toBe('geosite-samsung');
	});
});

describe('resolveRuleSetByTag', () => {
	it('prefers inline base over compiled companion ref', () => {
		const rs = resolveRuleSetByTag('geosite-samsung-srs', [
			{ tag: 'geosite-samsung', type: 'inline' },
			{ tag: 'geosite-samsung-srs', type: 'local' },
		]);
		expect(rs?.tag).toBe('geosite-samsung');
	});
});

describe('normalizeRuleForUI', () => {
	it('strips -srs from rule_set refs', () => {
		expect(
			normalizeRuleForUI({ rule_set: ['geosite-samsung-srs'], outbound: 'direct' }).rule_set,
		).toEqual(['geosite-samsung']);
	});
});

describe('normalizeRuleSetsForUI', () => {
	it('hides local companion when inline base exists', () => {
		const out = normalizeRuleSetsForUI([
			{ tag: 'geosite-samsung', type: 'inline', rules: [] },
			{ tag: 'geosite-samsung-srs', type: 'local', path: '/tmp/x.srs' },
		]);
		expect(out.map((rs) => rs.tag)).toEqual(['geosite-samsung']);
	});
});
