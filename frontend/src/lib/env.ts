export function envTruthy(value: unknown): boolean {
	return ['1', 'true', 'yes', 'on'].includes(String(value ?? '').trim().toLowerCase());
}

export function isMockDevMode(): boolean {
	return import.meta.env.DEV && envTruthy(import.meta.env.VITE_API_STRIP_PREFIX);
}
