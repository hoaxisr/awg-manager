/**
 * Действия над AWG-туннелями (вкл/выкл, удаление, ручной пинг, перенос в
 * серверы, экспорт конфигов) вместе с их loading-состоянием.
 *
 * Вынесено из routes/+page.svelte при расщеплении навигации v3: тот же срез
 * нужен И странице /awg/tunnels, И дашборду главной (карточки дашборда зовут
 * те же обработчики). Код перенесён дословно, изменены только обращения к
 * списку туннелей — он приходит геттером.
 */
import { tunnels } from '$lib/stores/tunnels';
import { notifications } from '$lib/stores/notifications';
import { api } from '$lib/api/client';
import type { TunnelListItem, TunnelReferencedError } from '$lib/types';

export interface AwgTunnelActions {
	readonly toggleLoading: Record<string, boolean>;
	readonly deleteLoading: Record<string, boolean>;
	readonly pingChecking: Record<string, boolean>;
	readonly exporting: boolean;
	deleteConfirmId: string | null;
	referencedDetails: TunnelReferencedError | null;
	referencedTunnelName: string;
	handleToggleOnOff(id: string): Promise<void>;
	requestDelete(id: string): void;
	handleDelete(id: string): Promise<void>;
	checkPing(id: string): Promise<void>;
	markAsServer(id: string): Promise<void>;
	handleExportAll(): Promise<void>;
}

export function createAwgTunnelActions(getAwgList: () => TunnelListItem[]): AwgTunnelActions {
	let toggleLoading = $state<Record<string, boolean>>({});
	let deleteLoading = $state<Record<string, boolean>>({});
	let pingChecking = $state<Record<string, boolean>>({});
	let exporting = $state(false);
	let deleteConfirmId = $state<string | null>(null);
	let referencedDetails = $state<TunnelReferencedError | null>(null);
	let referencedTunnelName = $state('');

	async function handleToggleOnOff(id: string) {
		const tunnel = getAwgList().find((t) => t.id === id);
		if (!tunnel) return;
		// needs_start is NOT "on" — it means "intent up but not actually running",
		// so the toggle should show OFF and the click should fire Start, not Stop.
		const isOn = ['running', 'starting', 'broken'].includes(tunnel.status);
		toggleLoading = { ...toggleLoading, [id]: true };
		try {
			if (isOn) {
				await tunnels.stop(id);
				notifications.success('Туннель остановлен');
			} else {
				await tunnels.start(id);
				notifications.success('Туннель запущен');
			}
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка');
		} finally {
			const { [id]: _, ...rest } = toggleLoading;
			toggleLoading = rest;
		}
	}

	async function handleDelete(id: string) {
		deleteConfirmId = null;
		deleteLoading = { ...deleteLoading, [id]: true };
		try {
			const result = await tunnels.remove(id);
			if (result.success && result.verified) {
				notifications.success('Туннель удалён');
			} else {
				notifications.error('Не удалось верифицировать удаление');
			}
		} catch (e) {
			if (e instanceof Error && e.message === 'tunnel_referenced') {
				const refErr = e as Error & { details: TunnelReferencedError };
				referencedDetails = refErr.details;
				referencedTunnelName = getAwgList().find((t) => t.id === id)?.name ?? id;
			} else {
				notifications.error(e instanceof Error ? e.message : 'Не удалось удалить туннель');
			}
		} finally {
			const { [id]: _, ...rest } = deleteLoading;
			deleteLoading = rest;
		}
	}

	async function checkPing(id: string) {
		if (pingChecking[id]) return;
		pingChecking[id] = true;
		try {
			const result = await api.checkConnectivity(id);
			tunnels.updateConnectivity(id, result.connected, result.latency ?? null);
		} catch {
			tunnels.updateConnectivity(id, false, null);
		} finally {
			pingChecking[id] = false;
		}
	}

	async function markAsServer(id: string) {
		try {
			await api.markServerInterface(id);
			// markServerInterface returns fresh ServersSnapshot; the tunnels
			// list also changes (the system card disappears) — invalidate.
			tunnels.invalidate();
			notifications.success(`Туннель ${id} перенесён в серверы.`);
		} catch (e) {
			notifications.error(e instanceof Error ? e.message : 'Ошибка переноса в серверы');
		}
	}

	async function handleExportAll() {
		exporting = true;
		try {
			const blob = await api.exportAllTunnels();
			const { downloadBlob } = await import('$lib/utils/download');
			downloadBlob(blob, 'awg-tunnels.zip');
		} catch {
			notifications.error('Не удалось экспортировать конфиги');
		} finally {
			exporting = false;
		}
	}

	return {
		get toggleLoading() { return toggleLoading; },
		get deleteLoading() { return deleteLoading; },
		get pingChecking() { return pingChecking; },
		get exporting() { return exporting; },
		get deleteConfirmId() { return deleteConfirmId; },
		set deleteConfirmId(v) { deleteConfirmId = v; },
		get referencedDetails() { return referencedDetails; },
		set referencedDetails(v) { referencedDetails = v; },
		get referencedTunnelName() { return referencedTunnelName; },
		set referencedTunnelName(v) { referencedTunnelName = v; },
		handleToggleOnOff,
		requestDelete: (id: string) => { deleteConfirmId = id; },
		handleDelete,
		checkPing,
		markAsServer,
		handleExportAll,
	};
}
