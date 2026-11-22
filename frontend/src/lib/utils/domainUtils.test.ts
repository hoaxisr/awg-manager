import { describe, expect, it } from 'vitest';
import {
	buildExternalLookupLinks,
	detectIpFamily,
	extractRootDomain,
	ipToHostCidr,
	snipLongDomain,
} from './domainUtils';

describe('extractRootDomain', () => {
	it('collapses standard subdomains and URLs to eTLD+1', () => {
		expect(extractRootDomain('cdn.api.youtube.com')).toBe('youtube.com');
		expect(extractRootDomain('https://m.youtube.com/watch?v=1')).toBe('youtube.com');
		expect(extractRootDomain('*.googlevideo.com')).toBe('googlevideo.com');
		expect(extractRootDomain('API.GitHub.COM.')).toBe('github.com');
	});

	it('preserves multi-label ccSLDs and shared hosting subdomains appropriately', () => {
		expect(extractRootDomain('cdn.static.example.co.uk')).toBe('example.co.uk');
		expect(extractRootDomain('mail.yandex.msk.ru')).toBe('yandex.msk.ru');
		expect(extractRootDomain('sub.user.github.io')).toBe('user.github.io');
		expect(extractRootDomain('sub.user.github.io', { keepHostingSubdomains: false })).toBe(
			'github.io',
		);
	});

	it('leaves IPs, CIDRs, and single-label hostnames intact', () => {
		expect(extractRootDomain('1.1.1.1')).toBe('1.1.1.1');
		expect(extractRootDomain('149.154.160.0/20')).toBe('149.154.160.0/20');
		expect(extractRootDomain('2606:4700:4700::1111')).toBe('2606:4700:4700::1111');
		expect(extractRootDomain('localhost')).toBe('localhost');
	});
});

describe('detectIpFamily & ipToHostCidr', () => {
	it('detects IPv4, IPv6, NAT64, and converts to host CIDR', () => {
		expect(detectIpFamily('1.1.1.1')).toBe('ipv4');
		expect(detectIpFamily('::ffff:8.8.8.8')).toBe('ipv4');
		expect(detectIpFamily('2606:4700:4700::1111')).toBe('ipv6');
		expect(detectIpFamily('64:ff9b::0808:0808')).toBe('nat64');
		expect(detectIpFamily('64:ff9b:1::8.8.8.8')).toBe('nat64');
		expect(detectIpFamily('youtube.com')).toBe('unknown');

		expect(ipToHostCidr('1.1.1.1')).toBe('1.1.1.1/32');
		expect(ipToHostCidr('::ffff:8.8.4.4')).toBe('8.8.4.4/32');
		expect(ipToHostCidr('2606:4700::1111')).toBe('2606:4700::1111/128');
		expect(ipToHostCidr('10.0.0.0/8')).toBe('10.0.0.0/8');
	});
});

describe('snipLongDomain', () => {
	it('keeps short domains intact and snips long ones losslessly', () => {
		expect(snipLongDomain('example.com', 40)).toEqual({
			snipped: false,
			prefix: 'example.com',
			middle: '',
			suffix: '',
		});

		const longHost = 'very-long-subdomain-123456.eu-west-1.compute.amazonaws.com';
		const snipped = snipLongDomain(longHost, 32);
		expect(snipped.snipped).toBe(true);
		expect(snipped.prefix + snipped.middle + snipped.suffix).toBe(longHost);
		expect(snipped.prefix.length).toBe(16);
		expect(snipped.suffix.length).toBe(16);
	});
});

describe('buildExternalLookupLinks', () => {
	it('includes BGP/RIPE/IPInfo only for public IPs and WHOIS for non-IP domains', () => {
		const pub = buildExternalLookupLinks({
			ip: '1.1.1.1',
			scope: 'public',
			domain: 'api.cloudflare.com',
		});
		expect(pub.map((l) => l.id)).toEqual(['bgp', 'ripe', 'ipinfo', 'whois']);
		expect(pub.find((l) => l.id === 'whois')?.url).toBe('https://who.is/whois/cloudflare.com');

		const priv = buildExternalLookupLinks({
			ip: '192.168.1.1',
			scope: 'private',
			domain: 'router.lan',
		});
		expect(priv.map((l) => l.id)).toEqual(['whois']);

		const fake = buildExternalLookupLinks({
			ip: '198.18.0.10',
			scope: 'benchmark',
		});
		expect(fake).toEqual([]);
	});
});
