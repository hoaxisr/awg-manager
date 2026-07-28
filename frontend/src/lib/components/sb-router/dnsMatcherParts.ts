import type { SingboxRouterDNSRule } from '$lib/types';

export interface DnsMatcherPart {
	key:
		| 'rule_set'
		| 'suffix'
		| 'domain'
		| 'keyword'
		| 'regex'
		| 'query_type'
		| 'source_ip'
		| 'match_response'
		| 'ip_cidr';
	value: string;
}

function headWithExtra(items: string[], stripLeadingDot = false): string {
	let head = items[0] ?? '';
	if (stripLeadingDot) head = head.replace(/^\./, '');
	const rest = items.length > 1 ? ` +${items.length - 1}` : '';
	return `${head}${rest}`;
}

/** Matcher fragments for DNS rule compact rows (order matches edit modal). */
export function dnsMatcherParts(r: SingboxRouterDNSRule): DnsMatcherPart[] {
	const parts: DnsMatcherPart[] = [];
	// match_response — матчер ответа (sing-box 1.14): true = анонимный evaluate
	// выше по цепочке, строка = тег конкретного evaluate. false из hand-edited
	// конфига — «выключено» (backend IsEnabled).
	if (r.match_response !== undefined && r.match_response !== false) {
		parts.push({
			key: 'match_response',
			value: r.match_response === true ? 'анонимный' : r.match_response,
		});
	}
	if (r.rule_set?.length) parts.push({ key: 'rule_set', value: r.rule_set.join(', ') });
	if (r.domain_suffix?.length) {
		parts.push({ key: 'suffix', value: headWithExtra(r.domain_suffix, true) });
	}
	if (r.domain?.length) parts.push({ key: 'domain', value: headWithExtra(r.domain) });
	if (r.domain_keyword?.length) parts.push({ key: 'keyword', value: headWithExtra(r.domain_keyword) });
	if (r.domain_regex?.length) parts.push({ key: 'regex', value: headWithExtra(r.domain_regex) });
	if (r.query_type?.length) parts.push({ key: 'query_type', value: r.query_type.join(', ') });
	// source_ip_cidr is a real DNS matcher backend-side (dnsRuleHasMatcher counts
	// it). It has no UI input, but a hand-edited source-scoped DNS rule must NOT
	// be mistaken for a matcher-less catch-all (bug #445 review): counting it here
	// keeps isCatchAllDnsRule / shadow detection in sync with the backend.
	if (r.source_ip_cidr?.length) {
		parts.push({ key: 'source_ip', value: headWithExtra(r.source_ip_cidr) });
	}
	if (r.ip_cidr?.length) {
		parts.push({ key: 'ip_cidr', value: r.ip_cidr.join(', ') });
	}
	return parts;
}

export function dnsMatcherSummary(r: SingboxRouterDNSRule): string {
	const parts = dnsMatcherParts(r);
	if (parts.length === 0) return '—';
	return parts
		.map((p) => (p.key === 'query_type' ? `query_type=${p.value}` : `${p.key}: ${p.value}`))
		.join(' · ');
}
