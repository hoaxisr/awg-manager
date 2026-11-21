const TWO_LABEL_CC_SLDS = new Set([
	'co.uk',
	'org.uk',
	'ac.uk',
	'gov.uk',
	'net.uk',
	'com.au',
	'net.au',
	'org.au',
	'edu.au',
	'gov.au',
	'co.jp',
	'ne.jp',
	'or.jp',
	'go.jp',
	'ac.jp',
	'com.br',
	'net.br',
	'org.br',
	'gov.br',
	'com.cn',
	'net.cn',
	'org.cn',
	'gov.cn',
	'com.tw',
	'org.tw',
	'com.hk',
	'co.kr',
	'or.kr',
	'co.nz',
	'net.nz',
	'org.nz',
	'co.za',
	'co.in',
	'net.in',
	'org.in',
	'com.tr',
	'net.tr',
	'org.tr',
	'gov.tr',
	'com.ua',
	'net.ua',
	'org.ua',
	'in.ua',
	'kiev.ua',
	'kyiv.ua',
	'com.ru',
	'net.ru',
	'org.ru',
	'pp.ru',
	'msk.ru',
	'spb.ru',
	'gov.ru',
	'edu.ru',
	'mil.ru',
	'com.kz',
	'org.kz',
	'com.by',
	'com.pl',
	'net.pl',
	'org.pl',
	'com.de',
	'com.es',
	'com.fr',
	'com.it',
	'com.mx',
	'com.ar',
	'com.sg',
	'com.my',
	'com.vn',
	'com.ph',
	'com.pk',
	'com.ng',
	'com.eg',
	'com.sa',
	'co.il',
	'co.id',
	'co.th',
	'eu.org',
]);

const SHARED_HOSTING_SUFFIXES = new Set([
	'github.io',
	'gitlab.io',
	'pages.dev',
	'workers.dev',
	'vercel.app',
	'netlify.app',
	'herokuapp.com',
	'appspot.com',
	'cloudfront.net',
	'amazonaws.com',
	'azurewebsites.net',
	'firebaseapp.com',
	'web.app',
	'blogspot.com',
]);

export type IpFamilyKind = 'ipv4' | 'ipv6' | 'nat64' | 'unknown';

export interface SnippedDomain {
	snipped: boolean;
	prefix: string;
	middle: string;
	suffix: string;
}

export interface ExternalLookupLink {
	id: 'bgp' | 'ripe' | 'ipinfo' | 'whois';
	label: string;
	title: string;
	url: string;
}

function isIPv4Literal(value: string): boolean {
	const parts = value.split('.');
	if (parts.length !== 4) return false;
	for (const part of parts) {
		if (!/^\d{1,3}$/.test(part)) return false;
		const num = Number(part);
		if (num < 0 || num > 255) return false;
	}
	return true;
}

function isIPv6Literal(value: string): boolean {
	if (!value.includes(':')) return false;
	if (!/^[0-9a-fA-F:]+$/.test(value)) {
		// Allow IPv4-mapped IPv6 like ::ffff:192.0.2.1
		const lastColon = value.lastIndexOf(':');
		if (lastColon < 0) return false;
		const v4Part = value.slice(lastColon + 1);
		const v6Head = value.slice(0, lastColon + 1);
		if (!isIPv4Literal(v4Part) || !/^[0-9a-fA-F:]+$/.test(v6Head)) return false;
	}
	const doubleColons = value.split('::').length - 1;
	if (doubleColons > 1) return false;
	const groups = value.split(':').filter(Boolean);
	return groups.length >= 1 && groups.length <= 8;
}

function stripUrlNoise(input: string): string {
	let s = input.trim().toLowerCase();
	if (!s) return '';

	s = s.replace(/^[a-z][a-z0-9+.-]*:\/\//i, '');
	s = s.split(/[/?#]/, 1)[0] ?? '';

	if (s.startsWith('[')) {
		const closeIdx = s.indexOf(']');
		if (closeIdx > 1) {
			s = s.slice(1, closeIdx);
		}
	} else {
		// Strip ":port" only when there is a single colon (host:port, not bare IPv6).
		const firstColon = s.indexOf(':');
		if (firstColon > 0 && s.indexOf(':', firstColon + 1) === -1 && !s.includes('/')) {
			s = s.slice(0, firstColon);
		}
	}

	return s.replace(/\.+$/, '');
}

/**
 * Extracts the registrable root domain (eTLD+1) from a hostname, URL, or wildcard.
 * Leaves IPv4, IPv6, CIDR, and single-label hostnames unchanged.
 */
export function extractRootDomain(
	input: string,
	options: { keepHostingSubdomains?: boolean } = {},
): string {
	const raw = input.trim();
	if (!raw) return '';

	const lowered = raw.toLowerCase();
	const slashIdx = lowered.indexOf('/');
	if (slashIdx > 0) {
		const addrPart = lowered.slice(0, slashIdx);
		const prefixLenPart = lowered.slice(slashIdx + 1);
		if (/^\d{1,3}$/.test(prefixLenPart) && (isIPv4Literal(addrPart) || isIPv6Literal(addrPart))) {
			return lowered;
		}
	}

	let host = stripUrlNoise(raw);
	if (!host) return '';

	// Keep IP literals unchanged.
	if (isIPv4Literal(host) || isIPv6Literal(host)) {
		return host;
	}

	host = host.replace(/^\*\./, '').replace(/^\.+/, '');
	if (!host) return '';

	const labels = host.split('.').filter(Boolean);
	if (labels.length <= 2) {
		return labels.join('.');
	}

	const lastTwo = `${labels[labels.length - 2]}.${labels[labels.length - 1]}`;
	const keepHosting = options.keepHostingSubdomains !== false;
	if (TWO_LABEL_CC_SLDS.has(lastTwo) || (keepHosting && SHARED_HOSTING_SUFFIXES.has(lastTwo))) {
		if (labels.length <= 3) {
			return labels.join('.');
		}
		return `${labels[labels.length - 3]}.${lastTwo}`;
	}

	return lastTwo;
}

/** Classifies a raw IP address string as ipv4, ipv6, nat64, or unknown. */
export function detectIpFamily(ip: string): IpFamilyKind {
	const trimmed = stripUrlNoise(ip);
	if (!trimmed || trimmed.includes('/')) return 'unknown';
	if (isIPv4Literal(trimmed)) return 'ipv4';
	if (/^::ffff:\d{1,3}(?:\.\d{1,3}){3}$/i.test(trimmed)) return 'ipv4';
	if (/^64:ff9b::/i.test(trimmed) || /^64:ff9b:1:/i.test(trimmed)) return 'nat64';
	if (isIPv6Literal(trimmed)) return 'ipv6';
	return 'unknown';
}

/** Converts a host IP address into a /32 or /128 host CIDR; passes CIDRs through unchanged. */
export function ipToHostCidr(ip: string): string {
	const trimmed = ip.trim();
	if (!trimmed) return '';
	if (trimmed.includes('/')) return trimmed;

	const clean = stripUrlNoise(trimmed);
	const mapped = clean.match(/^::ffff:(\d{1,3}(?:\.\d{1,3}){3})$/i);
	if (mapped && isIPv4Literal(mapped[1])) {
		return `${mapped[1]}/32`;
	}
	if (isIPv4Literal(clean)) {
		return `${clean}/32`;
	}
	if (isIPv6Literal(clean)) {
		return `${clean}/128`;
	}
	return clean;
}

/** Splits overly long domain names into prefix + hidden middle + suffix while preserving full copyable text. */
export function snipLongDomain(domain: string, maxLen = 40): SnippedDomain {
	if (!domain || domain.length <= maxLen) {
		return { snipped: false, prefix: domain, middle: '', suffix: '' };
	}
	const keep = Math.min(Math.floor(maxLen / 2), Math.floor((domain.length - 1) / 2));
	if (keep <= 0 || keep * 2 >= domain.length) {
		return { snipped: false, prefix: domain, middle: '', suffix: '' };
	}
	return {
		snipped: true,
		prefix: domain.slice(0, keep),
		middle: domain.slice(keep, domain.length - keep),
		suffix: domain.slice(domain.length - keep),
	};
}

/** Builds external lookup links (BGP.Tools, RIPE, IPInfo for public IPs; WHOIS for domains). */
export function buildExternalLookupLinks(opts: {
	ip?: string;
	scope?: string;
	domain?: string;
}): ExternalLookupLink[] {
	const links: ExternalLookupLink[] = [];
	const cleanIp = opts.ip ? stripUrlNoise(opts.ip) : '';
	const ipFamily = cleanIp ? detectIpFamily(cleanIp) : 'unknown';
	const isPublicScope = opts.scope === undefined || opts.scope === 'public';

	if (cleanIp && ipFamily !== 'unknown' && isPublicScope) {
		const encodedIp = encodeURIComponent(cleanIp);
		links.push(
			{
				id: 'bgp',
				label: 'BGP.Tools',
				title: `BGP.Tools · ${cleanIp}`,
				url: `https://bgp.tools/prefix/${encodedIp}`,
			},
			{
				id: 'ripe',
				label: 'RIPE',
				title: `RIPEStat / RDAP · ${cleanIp}`,
				url: `https://apps.db.ripe.net/db-web-ui/query?searchtext=${encodedIp}`,
			},
			{
				id: 'ipinfo',
				label: 'IPInfo',
				title: `IPInfo · ${cleanIp}`,
				url: `https://ipinfo.io/${encodedIp}`,
			},
		);
	}

	const rawDomain = opts.domain?.trim() ?? '';
	if (rawDomain && detectIpFamily(rawDomain) === 'unknown' && rawDomain.includes('.')) {
		const root = extractRootDomain(rawDomain, { keepHostingSubdomains: false });
		if (root && root.includes('.') && detectIpFamily(root) === 'unknown') {
			links.push({
				id: 'whois',
				label: 'WHOIS',
				title: `WHOIS · ${root}`,
				url: `https://who.is/whois/${encodeURIComponent(root)}`,
			});
		}
	}

	return links;
}
