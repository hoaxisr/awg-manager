import { describe, it, expect } from 'vitest';
import { dnsMatcherParts, dnsMatcherSummary } from './dnsMatcherParts';

describe('dnsMatcherParts', () => {
	it('collects all matcher kinds in stable order', () => {
		expect(
			dnsMatcherParts({
				query_type: ['A', 'AAAA'],
				domain_regex: ['^ads\\.'],
				domain_keyword: ['tracker'],
				domain: ['example.com', 'foo.com'],
				domain_suffix: ['.youtube.com'],
				rule_set: ['geosite-netflix', 'ads'],
			}).map((p) => p.key),
		).toEqual(['rule_set', 'suffix', 'domain', 'keyword', 'regex', 'query_type']);
	});

	it('summary uses query_type= and colon for other keys', () => {
		expect(
			dnsMatcherSummary({
				rule_set: ['geosite-netflix'],
				domain_suffix: ['.yt.com', '.google.com'],
				query_type: ['HTTPS'],
			}),
		).toBe('rule_set: geosite-netflix · suffix: yt.com +1 · query_type=HTTPS');
	});

	it('match_response и ip_cidr — матчеры', () => {
		const parts = dnsMatcherParts({ match_response: 'rd', ip_cidr: ['10.0.0.0/8'] });
		expect(parts.map((p) => p.key)).toEqual(['match_response', 'ip_cidr']);
	});

	it('match_response: true — анонимная форма', () => {
		const parts = dnsMatcherParts({ match_response: true });
		expect(parts[0].key).toBe('match_response');
	});

	it('empty rule → dash summary', () => {
		expect(dnsMatcherSummary({})).toBe('—');
		expect(dnsMatcherParts({})).toEqual([]);
	});
});
