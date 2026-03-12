import { z } from 'zod';
import { calcByteSize } from '$lib/utils/protocols';

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
    // Peer fields
    endpoint: z.string().min(1, 'Endpoint обязателен'),
    allowedIPs: z.string().min(1, 'AllowedIPs обязателен'),
    persistentKeepalive: z.coerce.number().int().min(0).max(65535).default(25),
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
}).refine(data => {
    const total = calcByteSize(data.i1) + calcByteSize(data.i2) +
        calcByteSize(data.i3) + calcByteSize(data.i4) + calcByteSize(data.i5);
    return total <= 4096;
}, { message: 'Суммарный размер I1-I5 не должен превышать 4096 байт', path: ['i1'] });

// Infer types from schemas
export type EditTunnel = z.infer<typeof editTunnelSchema>;
