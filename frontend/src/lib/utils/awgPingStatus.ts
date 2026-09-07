import type { TunnelListItem } from '$lib/types';

export type AwgPingStatusNote = { text: string; tone: 'recovering' | 'transitional' };
export type AwgPingLabelVariant = 'short' | 'full';
export type AwgToggleTint = 'recovering' | 'starting' | 'unreachable';

/** Label for the ping row during start / stop / ping-check recovery / broken. */
export function awgPingStatusNote(
	tunnel: Pick<TunnelListItem, 'status' | 'pingCheck' | 'statusDetails'>,
	variant: AwgPingLabelVariant = 'short',
): AwgPingStatusNote | null {
	switch (tunnel.status) {
		case 'starting':
			return { text: 'Запускается', tone: 'transitional' };
		case 'needs_stop':
			return { text: 'Остановка...', tone: 'transitional' };
		case 'broken':
			// Причина состояния приходит только у обфусцированных туннелей и
			// вытесняет общее «Сломан»: единственный видимый канал — эта метка.
			return { text: tunnel.statusDetails || 'Сломан', tone: 'recovering' };
	}

	if (tunnel.status === 'running' && tunnel.pingCheck.status === 'recovering') {
		const n = tunnel.pingCheck.restartCount;
		if (variant === 'full') {
			return {
				text: n > 0 ? `Восстановление (${n})` : 'Проверка связи...',
				tone: 'recovering',
			};
		}
		return {
			text: n > 0 ? `Восст. (${n})` : 'Восстановление...',
			tone: 'recovering',
		};
	}

	return null;
}

/** Orange recovering visuals — active ping recovery or broken tunnel. */
export function awgRecoveringVisual(
	tunnel: Pick<TunnelListItem, 'status' | 'pingCheck'>,
): boolean {
	if (tunnel.status === 'broken') return true;
	return tunnel.status === 'running' && tunnel.pingCheck.status === 'recovering';
}

/** Flip toggle tint — mirrors card borderState priority. */
export function awgToggleTint(
	tunnel: Pick<TunnelListItem, 'status' | 'pingCheck' | 'connectivityCheck'>,
	conn?: { connected: boolean; latency?: number | null },
): AwgToggleTint | undefined {
	if (awgRecoveringVisual(tunnel)) return 'recovering';
	if (tunnel.status === 'starting') return 'starting';
	if (awgConnectivityDown(tunnel, conn)) return 'unreachable';
	return undefined;
}

export function awgConnectivityCheckEnabled(
	tunnel: Pick<TunnelListItem, 'connectivityCheck'>,
): boolean {
	return (tunnel.connectivityCheck?.method ?? 'http') !== 'disabled';
}

/** Running tunnel with an enabled check that reported no path to the internet. */
export function awgConnectivityDown(
	tunnel: Pick<TunnelListItem, 'status' | 'connectivityCheck'>,
	conn: { connected: boolean; latency?: number | null } | undefined,
): boolean {
	if (tunnel.status !== 'running') return false;
	if (!awgConnectivityCheckEnabled(tunnel)) return false;
	return conn !== undefined && !conn.connected;
}

export function awgShowConnectivityRow(status: string): boolean {
	return (
		status === 'running' ||
		status === 'broken' ||
		status === 'starting' ||
		status === 'needs_stop'
	);
}

/** List layout: ping chip only when there is a numeric latency to show. */
export function awgListShowsPingButton(
	tunnel: Pick<TunnelListItem, 'status' | 'connectivityCheck' | 'pingCheck' | 'statusDetails'>,
	connectivity: { connected: boolean; latency: number | null } | undefined,
): boolean {
	if (tunnel.status !== 'running') return false;
	if (!awgConnectivityCheckEnabled(tunnel)) return false;
	if (awgPingStatusNote(tunnel)) return false;
	return connectivity?.connected === true && connectivity.latency !== null;
}
