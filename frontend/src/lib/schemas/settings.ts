import { z } from 'zod';

export const serverSettingsSchema = z.object({
    port: z.coerce.number().int().min(1).max(65535).default(2222),
    interface: z.string().default('0.0.0.0'),
});

export const pingCheckDefaultsSchema = z.object({
    method: z.enum(['http', 'icmp']).default('http'),
    target: z.string().default('8.8.8.8'),
    interval: z.coerce.number().int().min(5).max(3600).default(45),
    deadInterval: z.coerce.number().int().min(10).max(7200).default(120),
    failThreshold: z.coerce.number().int().min(1).max(100).default(3),
});

export const pingCheckSettingsSchema = z.object({
    enabled: z.boolean().default(true),
    defaults: pingCheckDefaultsSchema,
});

export const loggingSettingsSchema = z.object({
    enabled: z.boolean().default(false),
    maxAge: z.coerce.number().int().min(1).max(168).default(2),
});

export const settingsSchema = z.object({
    schemaVersion: z.number().optional(),
    authEnabled: z.boolean().default(true),
    server: serverSettingsSchema,
    pingCheck: pingCheckSettingsSchema,
    logging: loggingSettingsSchema,
    disableMemorySaving: z.boolean().default(false),
});

export type ServerSettingsForm = z.infer<typeof serverSettingsSchema>;
export type PingCheckDefaultsForm = z.infer<typeof pingCheckDefaultsSchema>;
export type PingCheckSettingsForm = z.infer<typeof pingCheckSettingsSchema>;
export type LoggingSettingsForm = z.infer<typeof loggingSettingsSchema>;
export type SettingsForm = z.infer<typeof settingsSchema>;
