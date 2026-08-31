import { describe, it, expect } from 'vitest';
import { detectVersion, calcScores, mtuCeilingForProfile, runChecks, type AwgIface } from './awgConfAnalyzer';

const baseH: AwgIface = { h1: '10', h2: '20', h3: '30', h4: '40' };

describe('detectVersion — AWG 3.0', () => {
	it('classifies header protection as AWG 3.0', () => {
		const v = detectVersion({ ...baseH, headerprotectionkey: 'a2V5', jc: '4', s1: '12', s2: '12' });
		expect(v.ver).toBe('AWG 3.0');
	});

	it('classifies any awg3 timer as AWG 3.0', () => {
		const v = detectVersion({ ...baseH, rekeyaftertime: '120-150' });
		expect(v.ver).toBe('AWG 3.0');
	});

	it('AWG 3.0 outranks AWG 2.0 params', () => {
		const v = detectVersion({ h1: '100-200', h2: '20', h3: '30', h4: '40', s3: '12', s4: '9', i1: 'sig', maxhandshakeattempts: '5' });
		expect(v.ver).toBe('AWG 3.0');
	});

	it('non-awg3 config keeps its old classification', () => {
		expect(detectVersion({ ...baseH, jc: '4', s1: '12', s2: '12', i1: 'sig' }).ver).toBe('AWG 1.5');
	});
});

describe('calcScores — AWG 3.0 scores strongest', () => {
	it('gives AWG 3.0 a lower DPI risk than AWG 2.0', () => {
		const iface: AwgIface = { ...baseH, jc: '4' };
		const dpi3 = calcScores(runChecks(iface, {}, { ver: 'AWG 3.0', desc: '', obfLevel: null, protocol: null }), iface, { ver: 'AWG 3.0', desc: '', obfLevel: null, protocol: null }).dpi;
		const dpi2 = calcScores(runChecks(iface, {}, { ver: 'AWG 2.0', desc: '', obfLevel: null, protocol: null }), iface, { ver: 'AWG 2.0', desc: '', obfLevel: null, protocol: null }).dpi;
		expect(dpi3).toBeLessThan(dpi2);
	});
});

describe('mtuCeilingForProfile — AWG 3.0', () => {
	it('has a ceiling for AWG 3.0', () => {
		expect(mtuCeilingForProfile({ ver: 'AWG 3.0', desc: '', obfLevel: null, protocol: null })).toBeGreaterThan(1000);
	});
});

// F74: после F56 бэкенд не отдаёт PrivateKey, поэтому построенный из
// GET-данных conf приходит без этой строки — и анализатор ставил каждому
// загруженному туннелю fail «ключ отсутствует или невалиден» с −10 баллами.
// Ложный вердикт диагностики, а не пропавшая строка показа.
describe('runChecks — скрытый бэкендом PrivateKey', () => {
	const ver = { ver: 'AWG 2.0', desc: '', obfLevel: null, protocol: null } as const;
	const pkCheck = (opts?: { privateKeyHidden?: boolean }) =>
		runChecks({ ...baseH, privatekey: '' }, {}, ver, opts).find((c) => c.title === 'PrivateKey')!;

	// Контроль: без флага поведение прежнее — чужой conf без ключа
	// действительно дефектен.
	it('без флага ключ считается отсутствующим: fail, 0 баллов', () => {
		const c = pkCheck();
		expect(c.status).toBe('fail');
		expect(c.pts).toBe(0);
	});

	it('со флагом ключ помечен скрытым: info, полные баллы', () => {
		const c = pkCheck({ privateKeyHidden: true });
		expect(c.status).toBe('info');
		expect(c.value).toBe('(скрыт)');
		expect(c.pts).toBe(10);
	});
});
