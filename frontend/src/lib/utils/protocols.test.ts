import { describe, it, expect } from 'vitest';
import {
	protocols,
	calcTotalChars,
	MAX_SIGNATURE_CHARS,
	resolveSignatureGenerateOptions,
	signatureHostPresetOptions,
} from './protocols';

describe('protocols catalog', () => {
	it('lists the five backend profiles in order, QUIC first', () => {
		expect(Object.keys(protocols)).toEqual(['quic_initial', 'stun', 'dns', 'dtls', 'sip']);
	});
	it('calcTotalChars matches backend TotalChars', () => {
		expect(calcTotalChars({ i1: '<t>', i2: '', i3: '', i4: '', i5: '<b 0xff>' })).toBe(11);
		// <r 4000> — 4000 байт нагрузки в 8 символах, лимит не задевает
		expect(calcTotalChars({ i1: '<r 4000>', i2: '', i3: '', i4: '', i5: '' })).toBeLessThan(MAX_SIGNATURE_CHARS);
	});
	it('resolveSignatureGenerateOptions handles default, presets, single and multi custom domains', () => {
		expect(resolveSignatureGenerateOptions('default', '')).toEqual({ ok: true });
		expect(resolveSignatureGenerateOptions('ru_whitelist', '')).toEqual({
			ok: true,
			opts: { hostPreset: 'ru_whitelist' },
		});
		expect(resolveSignatureGenerateOptions('custom', '  ')).toMatchObject({ ok: false });
		expect(resolveSignatureGenerateOptions('custom', 'yandex.ru')).toEqual({
			ok: true,
			opts: { hostPreset: 'custom', domain: 'yandex.ru' },
		});
		expect(resolveSignatureGenerateOptions('custom', 'vk.com, mail.ru; ok.ru')).toEqual({
			ok: true,
			opts: { hostPreset: 'custom', domains: ['vk.com', 'mail.ru', 'ok.ru'] },
		});
		expect(signatureHostPresetOptions().map((o) => o.value)).toEqual(['default', 'ru_whitelist', 'cdn', 'custom']);
	});
});
