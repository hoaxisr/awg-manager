import { describe, it, expect } from 'vitest';
import { protocols, calcByteSize, calcTotalSize } from './protocols';

describe('protocols catalog', () => {
	it('lists the five backend profiles in order, QUIC first', () => {
		expect(Object.keys(protocols)).toEqual(['quic_initial', 'stun', 'dns', 'dtls', 'sip']);
	});
	it('calcByteSize matches backend ByteSize', () => {
		expect(calcByteSize('<b 0x0102>')).toBe(2);
		expect(calcByteSize('<r 10><rc 3><rd 2><t>')).toBe(19);
		expect(calcTotalSize({ i1: '<t>', i2: '', i3: '', i4: '', i5: '<b 0xff>' })).toBe(5);
	});
});
