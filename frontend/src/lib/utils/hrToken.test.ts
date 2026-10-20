import { describe, it, expect } from 'vitest';
import { hrTokenError } from './hrToken';

// Ожидания сняты прогоном JS из /opt/bin/hrweb 1.68.0 (save → classifyLine +
// InputValidator.isValidToken); та же таблица — в TestHRwebTokenError (Go).
describe('hrTokenError', () => {
	it('принимает то, что принимает HRweb', () => {
		for (const t of [
			'a.com', 'Example.COM', 'xn--80ak6aa92e.com', 'sub-1.a.com', 'ru',
			'geosite:category-ads-all', 'geoip:ru', 'geoip:ru-x', '1.2.3.0/24',
			'2a00:1450::/32', '10.0.0.1-10.0.0.9',
		]) {
			expect(hrTokenError(t), t).toBeNull();
		}
	});

	it('отвергает символы, края и двойные разделители', () => {
		for (const t of [
			'::/0', '_dmarc.a.com', 'a_b', '*.a.com', '.a.com', 'a.com.', 'a..com', 'a-.com',
			'-a.com', 'geosite:google@cn', 'geosite:', 'a b.com', 'a.com:', '1.2.3.0/24-x',
			'1.2.3.0//24',
		]) {
			expect(hrTokenError(t), t).toBe('chars');
		}
	});

	it('требует маску у IP', () => {
		for (const t of ['1.2.3.4', '2a00::1', 'full:a.com']) {
			expect(hrTokenError(t), t).toBe('mask');
		}
	});
});
