import { describe, it, expect } from 'vitest';
import { readinessRows } from './readinessRows';

describe('readinessRows', () => {
	it('always emits the five FE-spec rows in order', () => {
		const rows = readinessRows({ running: true, active: true });
		expect(rows.map((r) => r.key)).toEqual([
			'singbox',
			'route',
			'source',
			'dns',
			'egress',
		]);
	});

	it('drives sing-box + route from the live signals', () => {
		const live = readinessRows({ running: true, active: true });
		expect(live.find((r) => r.key === 'singbox')?.state).toBe('ok');
		expect(live.find((r) => r.key === 'route')?.state).toBe('ok');

		const stopped = readinessRows({ running: false, active: false });
		expect(stopped.find((r) => r.key === 'singbox')?.state).toBe('error');
		expect(stopped.find((r) => r.key === 'route')?.state).toBe('error');
	});

	it('source-preservation is tri-state', () => {
		const ok = readinessRows({ running: true, active: true, sourcePreserved: true });
		expect(ok.find((r) => r.key === 'source')?.state).toBe('ok');

		const snat = readinessRows({ running: true, active: true, sourcePreserved: false });
		expect(snat.find((r) => r.key === 'source')?.state).toBe('warn');

		const unknown = readinessRows({ running: true, active: true });
		const sourceRow = unknown.find((r) => r.key === 'source');
		expect(sourceRow?.state).toBe('unknown');
		expect(sourceRow?.text).toBe('не определено');
	});

	it('DNS and egress are always reported as unknown (never fabricated)', () => {
		// Even with everything live, the two deferred rows stay honest.
		const rows = readinessRows({ running: true, active: true, sourcePreserved: true });
		const dns = rows.find((r) => r.key === 'dns');
		const egress = rows.find((r) => r.key === 'egress');
		expect(dns?.state).toBe('unknown');
		expect(dns?.text).toBe('не сообщается движком');
		expect(egress?.state).toBe('unknown');
		expect(egress?.text).toBe('не сообщается движком');
	});
});
