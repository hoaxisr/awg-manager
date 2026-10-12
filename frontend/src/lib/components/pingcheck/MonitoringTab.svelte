<!-- frontend/src/lib/components/pingcheck/MonitoringTab.svelte -->
<script lang="ts">
	import { m } from '$lib/i18n';
	import { onMount } from 'svelte';
	import { api } from '$lib/api/client';
	import { pingCheckStatus, pingCheckLogs, loadPingLogs } from '$lib/stores/pingcheck';
	import { groupLogsByTunnel, computeCardStats } from '$lib/utils/pingStats';
	import { WatchdogCard } from '$lib/components/pingcheck';
	import KernelPingCheckModal from '$lib/components/pingcheck/KernelPingCheckModal.svelte';
	import NativeWGPingCheckModal from '$lib/components/pingcheck/NativeWGPingCheckModal.svelte';
	import { EmptyState } from '$lib/components/layout';
	import { notifications } from '$lib/stores/notifications';
	import type { AWGTunnel, TunnelListItem, NativePingCheckStatus, NativePingCheckConfig } from '$lib/types';

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
			notifications.error(m.pingcheck_tab_load_failed());
		} finally {
			loading = false;
		}
	});

	// Догружаем конфиги для всех туннелей из статуса (включая выключенные) —
	// наличие cfg отличает «настроен, но выключен» от «никогда не настраивался».
	$effect(() => {
		const ids = statuses.map((s) => s.tunnelId);
		if (ids.length) loadConfigs(ids);
	});

	function metaFor(id: string): TunnelListItem | undefined {
		return tunnelMeta.find((t) => t.id === id);
	}

	function configLine(cfg: AWGTunnel['pingCheck'] | undefined, method: string, failThreshold: number): string {
		if (!cfg) return m.pingcheck_config_line_short({ method: method.toUpperCase(), threshold: failThreshold });
		return m.pingcheck_config_line_full({
			method: (cfg.method || method).toUpperCase(),
			target: cfg.target,
			interval: cfg.interval,
			threshold: cfg.failThreshold,
		});
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
				// Активный мониторинг ИЛИ есть сохранённый конфиг → «настроен».
				configured: isWatchdog || !!cfg,
				configLine: configLine(cfg, s.method, s.failThreshold),
				stats: computeCardStats(logsByTunnel.get(s.tunnelId) ?? [], s),
				latencyNote: s.latencyNote,
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
			notifications.success(m.pingcheck_check_started());
		} catch {
			notifications.error(m.pingcheck_check_start_failed());
		}
	}
	async function enablePingcheck(id: string, name: string, backend: 'kernel' | 'nativewg') {
		try {
			const t = await api.getTunnel(id);
			const pc = t.pingCheck;
			if (!pc) {
				notifications.error(m.pingcheck_no_saved_settings());
				return;
			}
			if (backend === 'nativewg') {
				const cfg: NativePingCheckConfig = {
					host: pc.target,
					mode: pc.method as 'icmp' | 'connect' | 'tls',
					updateInterval: pc.interval,
					maxFails: pc.failThreshold,
					minSuccess: pc.minSuccess,
					timeout: pc.timeout,
					restart: pc.restart,
				};
				if (pc.port) cfg.port = pc.port;
				await api.configureNativePingCheck(id, cfg);
			} else {
				pc.enabled = true;
				await api.updateTunnel(id, t);
			}
			notifications.success(m.pingcheck_watchdog_enabled({ name }));
			pingCheckStatus.refetch();
			loadPingLogs();
		} catch {
			notifications.error(m.pingcheck_watchdog_enable_failed());
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
			notifications.success(m.pingcheck_watchdog_disabled({ name }));
			pingCheckStatus.refetch();
			loadPingLogs();
		} catch {
			notifications.error(m.pingcheck_watchdog_disable_failed());
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
	<EmptyState title={m.pingcheck_no_tunnels_title()} description={m.pingcheck_no_tunnels_description()} />
{:else}
	<div class="wd-grid">
		{#each cards as c (c.id)}
			{#if c.kind === 'pc'}
				<WatchdogCard
					name={c.name}
					backend={c.backend}
					awgVersion={c.awgVersion}
					statusKind={c.statusKind}
					hasPingcheck={c.configured}
					isWatchdog={c.isWatchdog}
					configLine={c.configLine}
					stats={c.stats}
					latencyNote={c.latencyNote}
					onConfigure={() => openConfig(c.id, c.name, c.backend)}
					onCheckNow={checkNow}
					onDisable={() => disablePingcheck(c.id, c.name, c.backend)}
					onEnable={() => enablePingcheck(c.id, c.name, c.backend)}
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
					onEnable={() => {}}
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
