<!-- frontend/src/lib/components/pingcheck/MonitoringTab.svelte -->
<script lang="ts">
	import { onMount } from 'svelte';
	import { api } from '$lib/api/client';
	import { pingCheckStatus, pingCheckLogs, loadPingLogs } from '$lib/stores/pingcheck';
	import { groupLogsByTunnel, computeCardStats } from '$lib/utils/pingStats';
	import { WatchdogCard } from '$lib/components/pingcheck';
	import KernelPingCheckModal from '$lib/components/pingcheck/KernelPingCheckModal.svelte';
	import NativeWGPingCheckModal from '$lib/components/pingcheck/NativeWGPingCheckModal.svelte';
	import { EmptyState } from '$lib/components/layout';
	import { notifications } from '$lib/stores/notifications';
	import type { AWGTunnel, TunnelListItem, NativePingCheckStatus } from '$lib/types';

	// Метаданные туннелей (awgVersion + список) и полный pingCheck-конфиг.
	let tunnelMeta = $state<TunnelListItem[]>([]);
	let configs = $state<Record<string, AWGTunnel['pingCheck']>>({});
	let loading = $state(true);

	// Настройки (дровер)
	let editId = $state<string | null>(null);
	let editName = $state('');
	let editBackend = $state<'kernel' | 'nativewg'>('kernel');
	let editNativeStatus = $state<NativePingCheckStatus | null>(null);

	const statuses = $derived($pingCheckStatus.data ?? []);
	const logsByTunnel = $derived(groupLogsByTunnel($pingCheckLogs));

	async function loadConfigs(ids: string[]) {
		const next: Record<string, AWGTunnel['pingCheck']> = {};
		await Promise.all(
			ids.map(async (id) => {
				try {
					const t = await api.getTunnel(id);
					if (t.pingCheck) next[id] = t.pingCheck;
				} catch {
					/* туннель мог исчезнуть — пропускаем */
				}
			}),
		);
		configs = next;
	}

	onMount(async () => {
		try {
			await loadPingLogs();
			const snap = await api.getTunnelsAll();
			tunnelMeta = snap.tunnels ?? [];
		} catch (e) {
			notifications.error('Не удалось загрузить мониторинг');
		} finally {
			loading = false;
		}
	});

	// Догружаем конфиги для набора pingcheck-туннелей при изменении статусов.
	$effect(() => {
		const ids = statuses.filter((s) => s.enabled).map((s) => s.tunnelId);
		if (ids.length) loadConfigs(ids);
	});

	function metaFor(id: string): TunnelListItem | undefined {
		return tunnelMeta.find((t) => t.id === id);
	}

	function configLine(cfg: AWGTunnel['pingCheck'] | undefined, method: string, failThreshold: number): string {
		if (!cfg) return `${method.toUpperCase()} · порог ${failThreshold}`;
		return `${(cfg.method || method).toUpperCase()} → ${cfg.target} · ${cfg.interval}с · порог ${cfg.failThreshold}`;
	}

	// Карточки: pingcheck-туннели (из статуса) + туннели без pingcheck (из меты).
	const cards = $derived.by(() => {
		const pcIds = new Set(statuses.map((s) => s.tunnelId));
		const enabled = statuses.map((s) => {
			const cfg = configs[s.tunnelId];
			const meta = metaFor(s.tunnelId);
			const isWatchdog = s.enabled === true;
			return {
				kind: 'pc' as const,
				id: s.tunnelId,
				name: s.tunnelName,
				backend: s.backend,
				awgVersion: meta?.awgVersion,
				statusKind: s.status,
				isWatchdog,
				configLine: configLine(cfg, s.method, s.failThreshold),
				stats: computeCardStats(logsByTunnel.get(s.tunnelId) ?? [], s),
			};
		});
		const noPc = tunnelMeta
			.filter((t) => !pcIds.has(t.id))
			.map((t) => ({
				kind: 'note' as const,
				id: t.id,
				name: t.name,
				backend: (t as { backend?: 'kernel' | 'nativewg' }).backend ?? 'kernel',
				awgVersion: t.awgVersion,
			}));
		return [...enabled, ...noPc];
	});

	async function openConfig(id: string, name: string, backend: 'kernel' | 'nativewg') {
		editName = name;
		editBackend = backend;
		editNativeStatus = null;
		if (backend === 'nativewg') {
			try {
				editNativeStatus = await api.getNativePingCheckStatus(id);
			} catch {
				editNativeStatus = null;
			}
		}
		editId = id;
	}

	function closeConfig() {
		editId = null;
	}
	function afterSave() {
		editId = null;
		pingCheckStatus.refetch();
		loadPingLogs();
	}
	async function checkNow() {
		try {
			await api.triggerPingCheck();
			notifications.success('Проверка запущена');
		} catch {
			notifications.error('Не удалось запустить проверку');
		}
	}
	async function disablePingcheck(id: string, name: string, backend: 'kernel' | 'nativewg') {
		try {
			if (backend === 'nativewg') {
				await api.removeNativePingCheck(id);
			} else {
				const t = await api.getTunnel(id);
				if (t.pingCheck) t.pingCheck.enabled = false;
				await api.updateTunnel(id, t);
			}
			notifications.success(`Watchdog выключен: ${name}`);
			pingCheckStatus.refetch();
			loadPingLogs();
		} catch {
			notifications.error('Не удалось выключить watchdog');
		}
	}
</script>

{#if loading}
	<div class="wd-grid">
		{#each Array(4) as _, i (i)}
			<div class="wd-skel"></div>
		{/each}
	</div>
{:else if cards.length === 0}
	<EmptyState title="Нет туннелей" description="Создайте туннель, чтобы видеть мониторинг." />
{:else}
	<div class="wd-grid">
		{#each cards as c (c.id)}
			{#if c.kind === 'pc'}
				<WatchdogCard
					name={c.name}
					backend={c.backend}
					awgVersion={c.awgVersion}
					statusKind={c.statusKind}
					hasPingcheck={true}
					isWatchdog={c.isWatchdog}
					configLine={c.configLine}
					stats={c.stats}
					onConfigure={() => openConfig(c.id, c.name, c.backend)}
					onCheckNow={checkNow}
					onDisable={() => disablePingcheck(c.id, c.name, c.backend)}
				/>
			{:else}
				<WatchdogCard
					name={c.name}
					backend={c.backend}
					awgVersion={c.awgVersion}
					statusKind="disabled"
					hasPingcheck={false}
					isWatchdog={false}
					configLine=""
					stats={null}
					onConfigure={() => openConfig(c.id, c.name, c.backend)}
					onCheckNow={checkNow}
					onDisable={() => {}}
				/>
			{/if}
		{/each}
	</div>
{/if}

{#if editId && editBackend === 'kernel'}
	<KernelPingCheckModal open={true} tunnelId={editId} tunnelName={editName} onclose={closeConfig} onSaved={afterSave} />
{:else if editId && editBackend === 'nativewg'}
	<NativeWGPingCheckModal
		open={true}
		tunnelId={editId}
		tunnelName={editName}
		status={editNativeStatus}
		onclose={closeConfig}
		onSaved={afterSave}
		onRemoved={afterSave}
	/>
{/if}

<style>
	.wd-grid { display: grid; grid-template-columns: repeat(2, 1fr); gap: 16px; padding-top: 16px; }
	.wd-skel { height: 220px; border-radius: 12px; background: var(--color-surface-2, rgba(255, 255, 255, 0.03)); animation: wd-pulse 1.4s ease-in-out infinite; }
	@keyframes wd-pulse { 0%, 100% { opacity: 0.4; } 50% { opacity: 0.7; } }
	@media (max-width: 640px) { .wd-grid { grid-template-columns: 1fr; } }
</style>
