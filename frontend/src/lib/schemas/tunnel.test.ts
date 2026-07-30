import { describe, it, expect } from 'vitest';
import { editTunnelSchema } from './tunnel';

// Minimal valid payload; individual tests override `endpoint`.
function base(endpoint: string) {
    return {
        name: 'wg0',
        address: '10.0.0.2/32',
        endpoint,
        allowedIPs: '0.0.0.0/0',
    };
}

function endpointErr(endpoint: string): string | undefined {
    const res = editTunnelSchema.safeParse(base(endpoint));
    if (res.success) return undefined;
    return res.error.issues.find(i => i.path[0] === 'endpoint')?.message;
}

describe('editTunnelSchema endpoint', () => {
    it('accepts hostname:port', () => {
        expect(endpointErr('vpn.example.com:51820')).toBeUndefined();
    });

    it('accepts IPv4:port', () => {
        expect(endpointErr('1.2.3.4:51820')).toBeUndefined();
    });

    it('accepts bracketed IPv6:port', () => {
        expect(endpointErr('[2001:db8::1]:51820')).toBeUndefined();
    });

    it('rejects bare (unbracketed) IPv6', () => {
        expect(endpointErr('2001:db8::1:51820')).toBe(
            'IPv6 endpoint указывается в квадратных скобках: [2001:db8::1]:51820',
        );
    });

    it('rejects empty endpoint', () => {
        expect(endpointErr('')).toBe('Endpoint обязателен');
    });

    // Bracketed-form shape checks: content must be a plausible v6 literal
    // (hex digits/colons/dots, at least one colon) and the port 1-65535.
    it('accepts loopback and embedded-IPv4 v6 literals', () => {
        expect(endpointErr('[::1]:51820')).toBeUndefined();
        expect(endpointErr('[::ffff:192.0.2.1]:443')).toBeUndefined();
        expect(endpointErr('[2001:DB8::1]:65535')).toBeUndefined();
    });

    it('rejects empty brackets', () => {
        expect(endpointErr('[]:1')).toBeDefined();
    });

    it('rejects bracketed IPv6 without a port', () => {
        expect(endpointErr('[::1]')).toBeDefined();
        expect(endpointErr('[2001:db8::1]:')).toBeDefined();
    });

    it('rejects non-v6 garbage inside brackets', () => {
        expect(endpointErr('[junk]:51820')).toBeDefined();
        expect(endpointErr('[vpn.example.com]:51820')).toBeDefined();
        expect(endpointErr('[192.0.2.1]:51820')).toBeDefined(); // no colon → not v6
    });

    it('rejects out-of-range or non-numeric ports', () => {
        expect(endpointErr('[2001:db8::1]:0')).toBeDefined();
        expect(endpointErr('[2001:db8::1]:65536')).toBeDefined();
        expect(endpointErr('[2001:db8::1]:x')).toBeDefined();
        expect(endpointErr('[2001:db8::1]:51820 ')).toBeDefined(); // trailing junk
    });
});

// AWG 3.0 сделал keepalive диапазоном; форма должна принимать обе формы.
describe('editTunnelSchema persistentKeepalive', () => {
    function kaErr(value: string): string | undefined {
        const res = editTunnelSchema.safeParse({ ...base('1.2.3.4:51820'), persistentKeepalive: value });
        if (res.success) return undefined;
        return res.error.issues.find(i => i.path[0] === 'persistentKeepalive')?.message;
    }

    it('принимает число и диапазон', () => {
        expect(kaErr('25')).toBeUndefined();
        expect(kaErr('22-30')).toBeUndefined();
        expect(kaErr('0')).toBeUndefined();
    });

    it('отклоняет мусор и перевёрнутый диапазон', () => {
        expect(kaErr('abc')).toBeDefined();
        expect(kaErr('30-22')).toBeDefined();
        expect(kaErr('70000')).toBeDefined();
        expect(kaErr('22-')).toBeDefined();
    });
});

// AWG 3.0 timing/padding params are u16_range_t: an int or "min-max" range,
// each value 0-65535 and hi >= lo. Empty means "unset".
describe('editTunnelSchema awg3 range params', () => {
    function fieldErr(field: string, value: string): string | undefined {
        const res = editTunnelSchema.safeParse({ ...base('vpn.example.com:51820'), [field]: value });
        if (res.success) return undefined;
        return res.error.issues.find(i => i.path[0] === field)?.message;
    }

    const rangeFields = [
        'contentPaddingAddition', 'rekeyAfterTime', 'rekeyTimeout',
        'rejectAfterTime', 'keepaliveTimeout', 'maxHandshakeAttempts',
    ];

    for (const field of rangeFields) {
        it(`${field}: accepts empty, single int, and min-max range`, () => {
            expect(fieldErr(field, '')).toBeUndefined();
            expect(fieldErr(field, '120')).toBeUndefined();
            expect(fieldErr(field, '120-150')).toBeUndefined();
            expect(fieldErr(field, '0')).toBeUndefined();
        });

        it(`${field}: rejects garbage, reversed and out-of-range values`, () => {
            expect(fieldErr(field, 'abc')).toBeDefined();
            expect(fieldErr(field, '150-120')).toBeDefined(); // hi < lo
            expect(fieldErr(field, '70000')).toBeDefined();   // > 65535
            expect(fieldErr(field, '120-')).toBeDefined();
            expect(fieldErr(field, '-120')).toBeDefined();
        });
    }

    it('headerProtectionKey accepts empty and a base64 key', () => {
        expect(fieldErr('headerProtectionKey', '')).toBeUndefined();
        expect(fieldErr('headerProtectionKey', 'cGxhY2Vob2xkZXJrZXlwbGFjZWhvbGRlcmtleTEyMzQ=')).toBeUndefined();
    });
});
