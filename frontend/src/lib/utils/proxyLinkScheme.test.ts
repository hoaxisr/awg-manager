import { describe, expect, it } from 'vitest';
import { detectProxyLinkScheme } from './proxyLinkScheme';

describe('detectProxyLinkScheme', () => {
	it('отправляет wdtt:// и qwdtt:// в разбор WDTT', () => {
		expect(detectProxyLinkScheme('wdtt://host:5000?p=abc')).toBe('wdtt');
		expect(detectProxyLinkScheme('qwdtt://host:5000?p=abc')).toBe('wdtt');
	});

	it('отправляет freeturn:// в разбор FreeTurn', () => {
		expect(detectProxyLinkScheme('freeturn://eyJ2IjoxfQ')).toBe('freeturn');
	});

	it('считает http и https подпиской', () => {
		expect(detectProxyLinkScheme('https://sub.example.com/x')).toBe('subscription');
		expect(detectProxyLinkScheme('http://sub.example.com/x')).toBe('subscription');
	});

	it('не различает регистр схемы', () => {
		expect(detectProxyLinkScheme('WDTT://host')).toBe('wdtt');
		expect(detectProxyLinkScheme('QWdtt://host')).toBe('wdtt');
		expect(detectProxyLinkScheme('HTTPS://sub.example.com')).toBe('subscription');
	});

	it('терпит пробелы по краям', () => {
		expect(detectProxyLinkScheme('  wdtt://host  ')).toBe('wdtt');
		expect(detectProxyLinkScheme('\n freeturn://x \t')).toBe('freeturn');
	});

	it('пустая строка и мусор — неизвестная схема', () => {
		expect(detectProxyLinkScheme('')).toBe('unknown');
		expect(detectProxyLinkScheme('   ')).toBe('unknown');
		expect(detectProxyLinkScheme('host:5000')).toBe('unknown');
		expect(detectProxyLinkScheme('vless://uuid@host')).toBe('unknown');
		expect(detectProxyLinkScheme('wdtt:host')).toBe('unknown');
	});
});
