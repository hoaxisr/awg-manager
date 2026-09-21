/** Число адресов из ответа TRUSTTUNNEL_MULTI_ADDRESS, иначе 0. */
export function isTrustTunnelMultiAddress(e: unknown): number {
	const err = e as { status?: number; body?: { code?: string; data?: { addresses?: number } } };
	if (err?.status !== 422 || err.body?.code !== 'TRUSTTUNNEL_MULTI_ADDRESS') return 0;
	return err.body.data?.addresses ?? 0;
}
