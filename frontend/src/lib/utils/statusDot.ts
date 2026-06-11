import type { StatusDotVariant } from '$lib/components/ui/StatusDot.svelte';
import type { TunnelListItem } from '$lib/types';
import { awgConnectivityDown } from '$lib/utils/awgPingStatus';
import type { SingboxDelayState } from '$lib/utils/singboxDelay';

export type StatusDotPresentation = {
	variant: StatusDotVariant;
	pulse: boolean;
	label: string;
};

export function awgManagedStatusDot(
	tunnel: TunnelListItem,
	connectivity?: { connected: boolean; latency: number | null },
): StatusDotPresentation {
	if (tunnel.hasAddressConflict) {
		return { variant: 'error', pulse: false, label: 'Конфликт IP' };
	}
	if (awgConnectivityDown(tunnel, connectivity)) {
		return { variant: 'error', pulse: false, label: 'Нет связи' };
	}
	switch (tunnel.status) {
		case 'running':
			if (tunnel.pingCheck.status === 'recovering') {
				return { variant: 'warning', pulse: true, label: 'Восстанавливается' };
			}
			return { variant: 'success', pulse: false, label: 'Активен' };
		case 'broken':
			return { variant: 'broken', pulse: true, label: 'Сломан' };
		case 'starting':
			return { variant: 'warning', pulse: true, label: 'Запускается' };
		case 'needs_stop':
			return { variant: 'warning', pulse: true, label: 'Останавливается' };
		case 'needs_start':
			return { variant: 'muted', pulse: false, label: 'Остановлен' };
		case 'stopping':
			return { variant: 'warning', pulse: true, label: 'Останавливается' };
		case 'not_created':
			return { variant: 'muted', pulse: false, label: 'Не создан' };
		case 'disabled':
			return { variant: 'muted', pulse: false, label: 'Выключен' };
		default:
			return { variant: 'muted', pulse: false, label: tunnel.status || '—' };
	}
}

export function singboxDelayStatusDot(
	state: SingboxDelayState,
	running: boolean,
): StatusDotPresentation {
	if (!running) {
		return { variant: 'muted', pulse: false, label: 'stopped' };
	}
	switch (state) {
		case 'ok':
			return { variant: 'success', pulse: false, label: 'ok' };
		case 'slow':
			return { variant: 'warning', pulse: false, label: 'slow' };
		case 'fail':
			return { variant: 'error', pulse: false, label: 'fail' };
		default:
			return { variant: 'muted', pulse: false, label: 'unknown' };
	}
}

export function awgLedToStatusDot(
	ledColor: 'green' | 'yellow' | 'orange' | 'red' | 'gray' | string,
	pulse: boolean,
): { variant: StatusDotVariant; pulse: boolean } {
	const color = ledColor as 'green' | 'yellow' | 'orange' | 'red' | 'gray';
	switch (color) {
		case 'green':
			return { variant: 'success', pulse };
		case 'yellow':
			return { variant: 'warning', pulse };
		case 'orange':
			return { variant: 'broken', pulse };
		case 'red':
			return { variant: 'error', pulse };
		default:
			return { variant: 'muted', pulse: false };
	}
}
