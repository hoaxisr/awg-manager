// On-demand delay formatting for the Outbounds chip (FE-spec §4 honesty:
// delay is the ONLY runtime number shown per outbound, and only after an
// explicit test — never throughput/speed).
//
// The proxies test / proxies list report a delay in ms where 0 means
// "unreachable / timed out" (see SingboxProxyMember.lastDelay,
// SingboxProxiesTestResponse.delays). `undefined` means "not tested yet".

export type DelayHealth = 'ok' | 'down' | 'unknown';

/** Health bucket for the per-member health dot. */
export function delayHealth(delay: number | undefined | null): DelayHealth {
	if (delay === undefined || delay === null) return 'unknown';
	return delay > 0 ? 'ok' : 'down';
}

/** Human label for a delay value: «—» untested, «timeout» unreachable, «<n> ms». */
export function formatDelay(delay: number | undefined | null): string {
	if (delay === undefined || delay === null) return '—';
	if (delay <= 0) return 'таймаут';
	return `${Math.round(delay)} ms`;
}
