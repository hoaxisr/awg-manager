import { describe, it, expect } from 'vitest';
import { engineFatalHint, ENGINE_FATAL_FALLBACK } from './engineFatalHints';

describe('engineFatalHint', () => {
	const cases: [string, string][] = [
		['... Legacy Address Filter Fields in DNS rules is deprecated', 'IP-набор'],
		['FATAL[0000] start service: initialize cache-file: timeout', 'killall sing-box'],
		['initialize router: parse rule-set[2]: open /opt/x.srs: no such file or directory', 'пересоздайте'],
		['start service: ... outbound not found: awg-vpn0', 'несуществующий'],
		['missing fakeip record, try enable `experimental.cache_file`', 'FakeIP'],
		['initialize inbound[0]: listen tcp 0.0.0.0:51272: bind: address already in use', 'Порт'],
	];
	for (const [raw, needle] of cases) {
		it(`maps ${needle}`, () => {
			expect(engineFatalHint(raw)).toContain(needle);
		});
	}

	it('does NOT match rule-set hint across unrelated lines (multi-line tail)', () => {
		const tail =
			'INFO router: loaded rule-set[0] geoip-ru\n' +
			'FATAL service: start: open /opt/etc/AmneziaWG/awg0.conf: no such file or directory';
		expect(engineFatalHint(tail)).toBeNull();
	});

	it('returns null for unknown FATAL', () => {
		expect(engineFatalHint('FATAL[0000] something unfamiliar')).toBeNull();
	});
	it('returns null for empty/missing input', () => {
		expect(engineFatalHint('')).toBeNull();
		expect(engineFatalHint(null)).toBeNull();
		expect(engineFatalHint(undefined)).toBeNull();
	});
	it('exposes a non-empty fallback', () => {
		expect(ENGINE_FATAL_FALLBACK).toContain('Движок не смог запуститься');
	});
});
