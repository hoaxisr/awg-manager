// База знаний для быстрого распознавания сервисов, CDN и организаций по IP-адресам
import { m } from '$lib/i18n';

export interface IpKnowledgeInfo {
	title: string;
	org?: string;
	country?: string;
	countryCode?: string;
	category?: string;
}

interface CidrRule {
	net: number;
	mask: number;
	title: () => string;
	org: string;
	countryCode: string;
	category: string;
}

function parseIp(ip: string): number | null {
	const parts = ip.trim().split('.');
	if (parts.length !== 4) return null;
	let n = 0;
	for (let i = 0; i < 4; i++) {
		const b = parseInt(parts[i], 10);
		if (isNaN(b) || b < 0 || b > 255) return null;
		n = (n << 8) | b;
	}
	return n >>> 0;
}

function parseCidr(cidr: string): { net: number; mask: number } | null {
	const [ipPart, bitsPart] = cidr.split('/');
	const net = parseIp(ipPart);
	if (net === null) return null;
	const bits = parseInt(bitsPart, 10);
	if (isNaN(bits) || bits < 0 || bits > 32) return null;
	const mask = bits === 0 ? 0 : (~0 << (32 - bits)) >>> 0;
	return { net: (net & mask) >>> 0, mask };
}

function countryByCode(cc: string): string {
	switch (cc) {
		case 'US': return m.ip_know_country_us();
		case 'NL': return m.ip_know_country_nl();
		case 'SG': return m.ip_know_country_sg();
		case 'EU': return m.ip_know_country_eu();
		case 'RU': return m.ip_know_country_ru();
		case 'GB': return m.ip_know_country_gb();
		case 'KR': return m.ip_know_country_kr();
		case 'IL': return m.ip_know_country_il();
		case 'AP': return m.ip_know_country_ap();
		case 'DE': return m.ip_know_country_de();
		case 'FI': return m.ip_know_country_fi();
		case 'FR': return m.ip_know_country_fr();
		default: return cc;
	}
}

const rawRules: Array<{
	cidr: string;
	title: string | (() => string);
	org: string;
	cc: string;
	category: string;
}> = [
	// Google / YouTube
	{ cidr: '142.250.0.0/15', title: 'YouTube / Google', org: 'Google LLC', cc: 'US', category: 'media' },
	{ cidr: '172.217.0.0/16', title: 'YouTube / Google', org: 'Google LLC', cc: 'US', category: 'media' },
	{ cidr: '173.194.0.0/16', title: 'YouTube / Google', org: 'Google LLC', cc: 'US', category: 'media' },
	{ cidr: '209.85.128.0/17', title: 'YouTube Video CDN', org: 'Google LLC', cc: 'US', category: 'media' },
	{ cidr: '216.58.192.0/19', title: 'Google Search / API', org: 'Google LLC', cc: 'US', category: 'cloud' },
	{ cidr: '108.177.0.0/17', title: 'Google Video', org: 'Google LLC', cc: 'US', category: 'media' },
	{ cidr: '74.125.0.0/16', title: 'Google Services', org: 'Google LLC', cc: 'US', category: 'cloud' },
	{ cidr: '64.233.160.0/19', title: 'Google CDN', org: 'Google LLC', cc: 'US', category: 'cloud' },
	{ cidr: '66.102.0.0/20', title: 'Google Infra', org: 'Google LLC', cc: 'US', category: 'cloud' },
	{ cidr: '66.249.64.0/19', title: 'Google User Content', org: 'Google LLC', cc: 'US', category: 'cloud' },

	// Meta / WhatsApp / Instagram
	{ cidr: '31.13.64.0/18', title: 'Meta / WhatsApp', org: 'Meta Platforms, Inc.', cc: 'US', category: 'communication' },
	{ cidr: '157.240.0.0/16', title: 'Meta / Instagram', org: 'Meta Platforms, Inc.', cc: 'US', category: 'social' },
	{ cidr: '57.144.0.0/16', title: 'Meta Platforms', org: 'Meta Platforms, Inc.', cc: 'US', category: 'social' },
	{ cidr: '129.134.0.0/16', title: 'Meta Infra', org: 'Meta Platforms, Inc.', cc: 'US', category: 'social' },
	{ cidr: '185.89.216.0/22', title: 'Meta / WhatsApp Media', org: 'Meta Platforms, Inc.', cc: 'US', category: 'communication' },
	{ cidr: '69.171.224.0/19', title: 'Meta / Facebook CDN', org: 'Meta Platforms, Inc.', cc: 'US', category: 'social' },

	// Telegram
	{ cidr: '149.154.160.0/20', title: 'Telegram', org: 'Telegram Messenger Inc.', cc: 'NL', category: 'communication' },
	{ cidr: '91.108.4.0/22', title: 'Telegram', org: 'Telegram Messenger Inc.', cc: 'NL', category: 'communication' },
	{ cidr: '91.108.8.0/22', title: 'Telegram Media', org: 'Telegram Messenger Inc.', cc: 'SG', category: 'communication' },
	{ cidr: '91.108.12.0/22', title: 'Telegram DC4', org: 'Telegram Messenger Inc.', cc: 'NL', category: 'communication' },
	{ cidr: '91.108.16.0/22', title: 'Telegram DC5', org: 'Telegram Messenger Inc.', cc: 'SG', category: 'communication' },
	{ cidr: '91.108.20.0/22', title: 'Telegram DC2', org: 'Telegram Messenger Inc.', cc: 'NL', category: 'communication' },
	{ cidr: '91.108.56.0/22', title: 'Telegram', org: 'Telegram Messenger Inc.', cc: 'NL', category: 'communication' },

	// Cloudflare
	{ cidr: '104.16.0.0/12', title: 'Cloudflare CDN', org: 'Cloudflare, Inc.', cc: 'US', category: 'cloud' },
	{ cidr: '172.64.0.0/13', title: 'Cloudflare CDN', org: 'Cloudflare, Inc.', cc: 'US', category: 'cloud' },
	{ cidr: '162.158.0.0/15', title: 'Cloudflare Proxy', org: 'Cloudflare, Inc.', cc: 'US', category: 'cloud' },
	{ cidr: '198.41.128.0/17', title: 'Cloudflare Edge', org: 'Cloudflare, Inc.', cc: 'US', category: 'cloud' },
	{ cidr: '188.114.96.0/20', title: 'Cloudflare EU', org: 'Cloudflare, Inc.', cc: 'EU', category: 'cloud' },

	// VK / Одноклассники / Mail.ru
	{ cidr: '87.240.128.0/18', title: () => m.ip_know_title_vk(), org: 'VK LLC', cc: 'RU', category: 'social' },
	{ cidr: '95.213.0.0/16', title: () => m.ip_know_title_vk(), org: 'VK LLC', cc: 'RU', category: 'social' },
	{ cidr: '5.101.40.0/22', title: () => m.ip_know_title_ok(), org: 'VK LLC', cc: 'RU', category: 'social' },
	{ cidr: '95.163.0.0/16', title: 'Mail.ru / VK', org: 'VK LLC', cc: 'RU', category: 'communication' },

	// Яндекс
	{ cidr: '77.88.0.0/18', title: () => m.ip_know_title_yandex(), org: 'Yandex LLC', cc: 'RU', category: 'cloud' },
	{ cidr: '87.250.224.0/19', title: () => m.ip_know_title_yandex_kp(), org: 'Yandex LLC', cc: 'RU', category: 'media' },
	{ cidr: '94.139.240.0/20', title: () => m.ip_know_title_yandex_cloud(), org: 'Yandex.Cloud LLC', cc: 'RU', category: 'cloud' },
	{ cidr: '93.158.177.0/24', title: () => m.ip_know_title_yandex_infra(), org: 'Yandex LLC', cc: 'RU', category: 'cloud' },

	// Apple
	{ cidr: '17.0.0.0/8', title: 'Apple Services', org: 'Apple Inc.', cc: 'US', category: 'cloud' },

	// Microsoft
	{ cidr: '20.0.0.0/8', title: 'Microsoft / Azure', org: 'Microsoft Corporation', cc: 'US', category: 'cloud' },
	{ cidr: '40.64.0.0/10', title: 'Microsoft / Azure', org: 'Microsoft Corporation', cc: 'US', category: 'cloud' },
	{ cidr: '52.96.0.0/12', title: 'Microsoft Office 365', org: 'Microsoft Corporation', cc: 'US', category: 'cloud' },

	// GitHub / Fastly / CDN
	{ cidr: '140.82.112.0/20', title: 'GitHub', org: 'GitHub, Inc.', cc: 'US', category: 'dev' },
	{ cidr: '185.199.108.0/22', title: 'GitHub Pages', org: 'GitHub, Inc.', cc: 'US', category: 'dev' },
	{ cidr: '151.101.0.0/16', title: 'Fastly CDN', org: 'Fastly, Inc.', cc: 'US', category: 'cloud' },
	{ cidr: '185.76.0.0/16', title: 'Fastly / Discord', org: 'Fastly, Inc.', cc: 'US', category: 'communication' },
	{ cidr: '204.216.188.0/24', title: 'Fastly CDN', org: 'Fastly, Inc.', cc: 'US', category: 'cloud' },

	// Akamai / EdgeCast / Asian CDN
	{ cidr: '23.0.0.0/12', title: 'Akamai CDN', org: 'Akamai Technologies', cc: 'US', category: 'cloud' },
	{ cidr: '96.211.229.0/24', title: 'Akamai CDN', org: 'Akamai Technologies', cc: 'US', category: 'cloud' },
	{ cidr: '194.221.250.0/24', title: 'Akamai CDN', org: 'Akamai Technologies', cc: 'EU', category: 'cloud' },
	{ cidr: '110.172.149.0/24', title: 'EdgeCast CDN', org: 'Verizon Digital Media', cc: 'US', category: 'cloud' },
	{ cidr: '169.197.114.0/23', title: 'CDN77 / Datacamp', org: 'DataCamp Limited', cc: 'GB', category: 'cloud' },
	{ cidr: '195.181.160.0/19', title: 'CDN77 / DataCamp', org: 'DataCamp Limited', cc: 'GB', category: 'media' },
	{ cidr: '185.107.56.0/22', title: 'CDN77 / DataCamp', org: 'DataCamp Limited', cc: 'GB', category: 'media' },
	{ cidr: '31.3.152.0/21', title: 'M247 Cloud', org: 'M247 Ltd', cc: 'GB', category: 'cloud' },
	{ cidr: '211.239.0.0/16', title: 'KT Media CDN', org: 'Korea Telecom', cc: 'KR', category: 'media' },
	{ cidr: '212.143.0.0/16', title: 'Bezeq International', org: 'Bezeq Int.', cc: 'IL', category: 'isp' },
	{ cidr: '61.250.0.0/16', title: 'Asia Pacific CDN', org: 'APNIC Network', cc: 'AP', category: 'cloud' },

	// Hosting & Cloud (AWS, Hetzner, OVH, DigitalOcean, Scaleway)
	{ cidr: '3.0.0.0/9', title: 'Amazon AWS', org: 'Amazon.com, Inc.', cc: 'US', category: 'cloud' },
	{ cidr: '15.197.0.0/16', title: 'AWS CloudFront', org: 'Amazon.com, Inc.', cc: 'US', category: 'cloud' },
	{ cidr: '18.0.0.0/9', title: 'Amazon AWS', org: 'Amazon.com, Inc.', cc: 'US', category: 'cloud' },
	{ cidr: '52.0.0.0/10', title: 'Amazon AWS', org: 'Amazon.com, Inc.', cc: 'US', category: 'cloud' },
	{ cidr: '54.0.0.0/9', title: 'Amazon AWS', org: 'Amazon.com, Inc.', cc: 'US', category: 'cloud' },
	{ cidr: '108.214.136.0/24', title: 'Amazon AWS', org: 'Amazon.com, Inc.', cc: 'US', category: 'cloud' },
	{ cidr: '88.198.0.0/16', title: 'Hetzner Cloud', org: 'Hetzner Online GmbH', cc: 'DE', category: 'cloud' },
	{ cidr: '138.201.0.0/16', title: 'Hetzner Cloud', org: 'Hetzner Online GmbH', cc: 'DE', category: 'cloud' },
	{ cidr: '144.76.0.0/16', title: 'Hetzner Cloud', org: 'Hetzner Online GmbH', cc: 'DE', category: 'cloud' },
	{ cidr: '159.69.0.0/16', title: 'Hetzner Cloud', org: 'Hetzner Online GmbH', cc: 'DE', category: 'cloud' },
	{ cidr: '168.119.0.0/16', title: 'Hetzner Cloud', org: 'Hetzner Online GmbH', cc: 'DE', category: 'cloud' },
	{ cidr: '95.216.0.0/15', title: 'Hetzner Cloud', org: 'Hetzner Online GmbH', cc: 'FI', category: 'cloud' },
	{ cidr: '116.202.0.0/15', title: 'Hetzner Cloud', org: 'Hetzner Online GmbH', cc: 'DE', category: 'cloud' },
	{ cidr: '49.12.0.0/15', title: 'Hetzner Cloud', org: 'Hetzner Online GmbH', cc: 'DE', category: 'cloud' },
	{ cidr: '65.108.0.0/15', title: 'Hetzner Cloud', org: 'Hetzner Online GmbH', cc: 'FI', category: 'cloud' },
	{ cidr: '198.199.64.0/18', title: 'DigitalOcean', org: 'DigitalOcean, LLC', cc: 'US', category: 'cloud' },
	{ cidr: '159.203.0.0/16', title: 'DigitalOcean', org: 'DigitalOcean, LLC', cc: 'US', category: 'cloud' },
	{ cidr: '51.89.244.0/24', title: 'OVHcloud', org: 'OVH SAS', cc: 'FR', category: 'cloud' },
	{ cidr: '146.239.26.0/24', title: 'OVHcloud', org: 'OVH SAS', cc: 'FR', category: 'cloud' },
	{ cidr: '51.15.225.0/24', title: 'Scaleway Cloud', org: 'Scaleway S.A.S.', cc: 'FR', category: 'cloud' },
	{ cidr: '217.160.165.0/24', title: 'IONOS Hosting', org: 'IONOS SE', cc: 'DE', category: 'cloud' },
	{ cidr: '37.19.210.0/24', title: 'M247 Hosting', org: 'M247 Ltd', cc: 'EU', category: 'cloud' },
	{ cidr: '67.144.0.0/16', title: 'Lumen / CenturyLink', org: 'Lumen Technologies', cc: 'US', category: 'isp' },

	// Steam / Valve
	{ cidr: '155.133.224.0/19', title: 'Steam (Valve)', org: 'Valve Corporation', cc: 'US', category: 'gaming' },
	{ cidr: '162.254.192.0/19', title: 'Steam (Valve)', org: 'Valve Corporation', cc: 'US', category: 'gaming' },
];

const compiledRules: CidrRule[] = [];
for (const r of rawRules) {
	const c = parseCidr(r.cidr);
	if (c) {
		const rawTitle = r.title;
		compiledRules.push({
			net: c.net,
			mask: c.mask,
			title: typeof rawTitle === 'function' ? rawTitle : () => rawTitle,
			org: r.org,
			countryCode: r.cc,
			category: r.category,
		});
	}
}

/**
 * Быстрое сопоставление IP-адреса с базой сервисов и стран
 */
export function lookupIpKnowledge(ipStr: string): IpKnowledgeInfo {
	if (!ipStr) {
		return {
			title: m.ip_know_ext_node(),
			org: m.ip_know_org_internet(),
			country: m.ip_know_country_ext(),
			countryCode: '🌐',
			category: 'external',
		};
	}
	let cleanIp = ipStr.trim();
	if (cleanIp.startsWith('[')) {
		const end = cleanIp.indexOf(']');
		if (end > 0) cleanIp = cleanIp.substring(1, end);
	} else {
		cleanIp = cleanIp.split(':')[0].trim();
	}
	const num = parseIp(cleanIp);
	if (num === null) {
		return {
			title: cleanIp || m.ip_know_ext_node(),
			org: m.ip_know_org_net_node(),
			country: m.ip_know_country_net(),
			countryCode: '🌐',
			category: 'external',
		};
	}

	for (const rule of compiledRules) {
		if ((num & rule.mask) === rule.net) {
			return {
				title: rule.title(),
				org: rule.org,
				country: countryByCode(rule.countryCode),
				countryCode: rule.countryCode,
				category: rule.category,
			};
		}
	}
	return {
		title: m.ip_know_ext_node(),
		org: m.ip_know_org_internet_svc(),
		country: m.ip_know_country_ext(),
		countryCode: '🌐',
		category: 'external',
	};
}

