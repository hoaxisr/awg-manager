import { describe, it, expect } from 'vitest';
import { parseAWG } from './awgConfAnalyzer';

describe('parseAWG', () => {
	it('ключи нормализуются в нижний регистр, секции разделяются', () => {
		const p = parseAWG('[Interface]\nPrivateKey = a\nRandomTrailers = on\n[Peer]\nPublicKey = b\nPersistentKeepalive = 25-35\n');
		expect(p.iface.privatekey).toBe('a');
		expect(p.iface.randomtrailers).toBe('on');
		expect(p.peer.persistentkeepalive).toBe('25-35');
	});

	it('без [Interface] — ошибка', () => {
		expect(() => parseAWG('[Peer]\nPublicKey = b\n')).toThrow();
	});
});
