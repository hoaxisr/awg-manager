import { z } from 'zod';
import { m } from '$lib/i18n';
import { MAX_SIGNATURE_CHARS } from '$lib/utils/protocols';

// Header protection takes its 12-byte nonce from the front of the Sx junk
// padding (S1 initiation, S2 response, S3 cookie, S4 transport), so shorter
// padding leaves the two sides with different nonces and every packet drops.
export const HEADER_PROTECTION_MIN_PADDING = 12;

// Значение u16_range из AWG 3.0: число или диапазон "min-max", обе границы
// 0-65535, верхняя не меньше нижней. Пустая строка означает "не задано".
function isU16Range(v: string): boolean {
    if (v === '') return true;
    const parts = v.match(/^(\d{1,5})(?:-(\d{1,5}))?$/);
    if (!parts) return false;
    const lo = Number(parts[1]);
    if (lo > 65535) return false;
    if (parts[2] !== undefined) {
        const hi = Number(parts[2]);
        if (hi > 65535 || hi < lo) return false;
    }
    return true;
}

const u16RangeField = () =>
    z.string().default('').refine(isU16Range, { error: () => m.validation_u16_range() });

// Edit tunnel schema - flat structure matching the edit form
export const editTunnelSchema = z.object({
    name: z.string()
        .min(1, { error: () => m.validation_name_required() })
        .max(15, { error: () => m.validation_name_max() })
        .regex(/^[a-zA-Z][a-zA-Z0-9_-]*$/, { error: () => m.validation_name_start_letter() }),
    ispInterface: z.string().default(''),
    // Interface fields
    address: z.string().min(1, { error: () => m.validation_address_required() }),
    mtu: z.coerce.number().int().min(576).max(65535).default(1280),
    dns: z.string().default('').refine(val => {
        if (!val) return true;
        return val.split(',').every(s => {
            const trimmed = s.trim();
            return trimmed === '' || /^(\d{1,3}\.){3}\d{1,3}$/.test(trimmed) || /^[0-9a-fA-F:]+$/.test(trimmed);
        });
    }, { error: () => m.validation_dns_list() }),
    // Peer fields
    // Accepts host:port, IPv4:port, and [IPv6]:port. IPv6 literals MUST be
    // bracketed — a bare "2001:db8::1:51820" is ambiguous with the port
    // separator (and awg_proxy.ko rejects it). Bracketed form is checked
    // for shape: non-empty v6-ish content (hex digits/colons/dots, at
    // least one colon) plus a 1-65535 port. Hostnames/IPv4 stay lax as
    // before: at most one colon (the host:port separator).
    endpoint: z.string().min(1, { error: () => m.validation_endpoint_required() }).refine(val => {
        if (val.startsWith('[')) {
            const bracketed = val.match(/^\[([0-9a-fA-F:.]+)\]:(\d{1,5})$/);
            if (!bracketed || !bracketed[1].includes(':')) return false; // empty/garbage brackets or no port
            const port = Number(bracketed[2]);
            return port >= 1 && port <= 65535;
        }
        // No brackets: at most one colon (the host:port separator) is allowed.
        return (val.match(/:/g) || []).length <= 1;
    }, { error: () => m.validation_endpoint_ipv6_brackets() }),
    allowedIPs: z.string().min(1, { error: () => m.validation_allowed_ips_required() }),
    // В AWG 3.0 keepalive стал диапазоном "min-max", из которого пир берёт
    // случайное значение на каждый взвод таймера. Диапазон принимают оба
    // бэкенда: kernel применяет его целиком, NativeWG отдаёт прошивке нижнюю
    // границу (storage.Keepalive.Effective).
    //
    // Нулевая нижняя граница отвергается отдельным предикатом, а не правкой
    // isU16Range: 0 означает «keepalive выключен», и с диапазоном это
    // противоречие — а вот у device-параметров AWG 3.0 нулевая нижняя граница
    // законна (ContentPaddingAddition = 0-64), и общий предикат им нужен как
    // есть. Зеркало config.ValidateKeepaliveSubmitted на бэкенде.
    persistentKeepalive: z.coerce.string().default('25')
        .refine(isU16Range, { error: () => m.validation_u16_range() })
        .refine(v => !(v.includes('-') && Number(v.split('-')[0]) === 0),
            { error: () => m.validation_keepalive_zero_range() }),
    // AWG params
    jc: z.coerce.number().int().min(1).max(128).default(4),
    jmin: z.coerce.number().int().min(0).max(1280).default(40),
    jmax: z.coerce.number().int().min(0).max(1280).default(70),
    s1: z.coerce.number().int().min(0).max(255).default(0),
    s2: z.coerce.number().int().min(0).max(255).default(0),
    s3: z.coerce.number().int().min(0).max(255).default(0),
    s4: z.coerce.number().int().min(0).max(255).default(0),
    h1: z.string().default(''),
    h2: z.string().default(''),
    h3: z.string().default(''),
    h4: z.string().default(''),
    i1: z.string().default(''),
    i2: z.string().default(''),
    i3: z.string().default(''),
    i4: z.string().default(''),
    i5: z.string().default(''),
    // AWG 3.0 device params (kernel mode only). headerProtectionKey is a
    // base64 key; the rest are u16 int-or-range values.
    headerProtectionKey: z.string().default(''),
    contentPaddingAddition: u16RangeField(),
    rekeyAfterTime: u16RangeField(),
    rekeyTimeout: u16RangeField(),
    rejectAfterTime: u16RangeField(),
    keepaliveTimeout: u16RangeField(),
    maxHandshakeAttempts: u16RangeField(),
    // AWG 3.1 device flags — read-only, set only by an imported .conf.
    randomTrailers: z.boolean().default(false),
    disableCookies: z.boolean().default(false),
}).refine(data => {
    return (data.i1 + data.i2 + data.i3 + data.i4 + data.i5).length <= MAX_SIGNATURE_CHARS;
}, { error: () => m.validation_signature_total_length({ max: MAX_SIGNATURE_CHARS }), path: ['i1'] })
    .refine(data => !data.headerProtectionKey ||
        [data.s1, data.s2, data.s3, data.s4].every(v => v >= HEADER_PROTECTION_MIN_PADDING), {
        error: () => m.validation_header_protection_padding({ min: HEADER_PROTECTION_MIN_PADDING }),
        path: ['headerProtectionKey'],
    });

// Infer types from schemas
export type EditTunnel = z.infer<typeof editTunnelSchema>;
