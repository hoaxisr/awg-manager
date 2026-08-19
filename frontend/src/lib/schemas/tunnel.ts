import { z } from 'zod';
import { calcByteSize } from '$lib/utils/protocols';

// Header protection takes its 12-byte nonce from the front of the Sx junk
// padding (S1 initiation, S2 response, S3 cookie, S4 transport), so shorter
// padding leaves the two sides with different nonces and every packet drops.
export const HEADER_PROTECTION_MIN_PADDING = 12;

// Значение u16_range из AWG 3.0: число или диапазон "min-max", обе границы
// 0-65535, верхняя не меньше нижней. Пустая строка означает "не задано".
function isU16Range(v: string): boolean {
    if (v === '') return true;
    const m = v.match(/^(\d{1,5})(?:-(\d{1,5}))?$/);
    if (!m) return false;
    const lo = Number(m[1]);
    if (lo > 65535) return false;
    if (m[2] !== undefined) {
        const hi = Number(m[2]);
        if (hi > 65535 || hi < lo) return false;
    }
    return true;
}

const u16RangeField = () =>
    z.string().default('').refine(isU16Range, { message: 'Укажите число 0-65535 или диапазон min-max' });

// Edit tunnel schema - flat structure matching the edit form
export const editTunnelSchema = z.object({
    name: z.string()
        .min(1, 'Название обязательно')
        .max(15, 'Максимум 15 символов')
        .regex(/^[a-zA-Z][a-zA-Z0-9_-]*$/, 'Должно начинаться с буквы'),
    ispInterface: z.string().default(''),
    // Interface fields
    address: z.string().min(1, 'Адрес обязателен'),
    mtu: z.coerce.number().int().min(576).max(65535).default(1280),
    dns: z.string().default('').refine(val => {
        if (!val) return true;
        return val.split(',').every(s => {
            const trimmed = s.trim();
            return trimmed === '' || /^(\d{1,3}\.){3}\d{1,3}$/.test(trimmed) || /^[0-9a-fA-F:]+$/.test(trimmed);
        });
    }, { message: 'Введите IP-адреса через запятую (например, 1.1.1.1, 8.8.8.8)' }),
    // Peer fields
    // Accepts host:port, IPv4:port, and [IPv6]:port. IPv6 literals MUST be
    // bracketed — a bare "2001:db8::1:51820" is ambiguous with the port
    // separator (and awg_proxy.ko rejects it). Bracketed form is checked
    // for shape: non-empty v6-ish content (hex digits/colons/dots, at
    // least one colon) plus a 1-65535 port. Hostnames/IPv4 stay lax as
    // before: at most one colon (the host:port separator).
    endpoint: z.string().min(1, 'Endpoint обязателен').refine(val => {
        if (val.startsWith('[')) {
            const m = val.match(/^\[([0-9a-fA-F:.]+)\]:(\d{1,5})$/);
            if (!m || !m[1].includes(':')) return false; // empty/garbage brackets or no port
            const port = Number(m[2]);
            return port >= 1 && port <= 65535;
        }
        // No brackets: at most one colon (the host:port separator) is allowed.
        return (val.match(/:/g) || []).length <= 1;
    }, { message: 'IPv6 endpoint указывается в квадратных скобках: [2001:db8::1]:51820' }),
    allowedIPs: z.string().min(1, 'AllowedIPs обязателен'),
    // В AWG 3.0 keepalive стал диапазоном "min-max", из которого пир берёт
    // случайное значение на каждый взвод таймера; NativeWG диапазон не примет,
    // это проверяет бэкенд.
    persistentKeepalive: z.coerce.string().default('25')
        .refine(isU16Range, { message: 'Укажите число 0-65535 или диапазон min-max' }),
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
    const total = calcByteSize(data.i1) + calcByteSize(data.i2) +
        calcByteSize(data.i3) + calcByteSize(data.i4) + calcByteSize(data.i5);
    return total <= 4096;
}, { message: 'Суммарный размер I1-I5 не должен превышать 4096 байт', path: ['i1'] })
    .refine(data => !data.headerProtectionKey ||
        [data.s1, data.s2, data.s3, data.s4].every(v => v >= HEADER_PROTECTION_MIN_PADDING), {
        message: `При заданном HeaderProtectionKey значения S1-S4 должны быть не меньше ${HEADER_PROTECTION_MIN_PADDING} — из этих байт берётся nonce`,
        path: ['headerProtectionKey'],
    });

// Infer types from schemas
export type EditTunnel = z.infer<typeof editTunnelSchema>;
