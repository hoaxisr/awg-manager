import { z } from 'zod';

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
});

// Create tunnel schema - flat structure for manual creation form
export const createTunnelSchema = z.object({
    name: z.string().default(''),
    privateKey: z.string().min(1, 'Приватный ключ обязателен'),
    address: z.string().default('10.0.0.2/32'),
    mtu: z.coerce.number().int().min(576).max(65535).default(1280),
    publicKey: z.string().min(1, 'Публичный ключ обязателен'),
    endpoint: z.string().min(1, 'Endpoint обязателен'),
    allowedIPs: z.string().default('0.0.0.0/0, ::/0'),
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
});

// Infer types from schemas
export type EditTunnel = z.infer<typeof editTunnelSchema>;
export type CreateTunnel = z.infer<typeof createTunnelSchema>;
