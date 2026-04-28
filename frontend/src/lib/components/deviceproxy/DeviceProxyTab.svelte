<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import {
		deviceProxyConfig,
		deviceProxyOutbounds,
		deviceProxyRuntime,
		deviceProxyMissingTarget,
	} from '$lib/stores/deviceproxy';
	import { SideDrawer } from '$lib/components/ui';
	import ActiveTunnelCard from './ActiveTunnelCard.svelte';
	import SettingsCard from './SettingsCard.svelte';
	import DeviceProxyStatRow from './DeviceProxyStatRow.svelte';
	import DeviceProxyClientInfoCard from './DeviceProxyClientInfoCard.svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import type { DeviceProxyConfig } from '$lib/types';

	interface ListenChoices {
		lanIP: string;
		bridges: { id: string; label: string; ip: string }[];
		singboxRunning: boolean;
	}

	let unsubConfig: (() => void) | null = null;
	let unsubOutbounds: (() => void) | null = null;
	let unsubRuntime: (() => void) | null = null;
	let choices = $state<ListenChoices | null>(null);
	let settingsDrawerOpen = $state(false);

	onMount(() => {
		unsubConfig = deviceProxyConfig.subscribe(() => {});
		unsubOutbounds = deviceProxyOutbounds.subscribe(() => {});
		unsubRuntime = deviceProxyRuntime.subscribe(() => {});
		api.getDeviceProxyListenChoices().then((v) => {
			choices = v;
		}).catch(() => {});
	});
	onDestroy(() => {
		unsubConfig?.();
		unsubOutbounds?.();
		unsubRuntime?.();
	});

	const configSnap = $derived($deviceProxyConfig);
	const outboundsSnap = $derived($deviceProxyOutbounds);
	const runtimeSnap = $derived($deviceProxyRuntime);

	const config = $derived<DeviceProxyConfig | null>(configSnap.data ?? null);
	const outbounds = $derived(outboundsSnap.data ?? []);
	const runtime = $derived(runtimeSnap.data ?? { alive: false, activeTag: '', defaultTag: '' });

	const missingTag = $derived($deviceProxyMissingTarget);

	const bridgeInterfaces = $derived(
		(choices?.bridges ?? [{ id: 'Bridge0', label: 'Bridge0' }]).map((b) => ({ id: b.id, label: b.label })),
	);

	const bridgeLabel = $derived.by(() => {
		if (!config || !choices) return '';
		const match = choices.bridges.find((b) => b.id === config.listenInterface);
		return match?.label ?? config.listenInterface;
	});

	const resolvedListenIP = $derived.by(() => {
		if (!config || !choices) return '';
		if (config.listenAll) return choices.lanIP || '';
		const match = choices.bridges.find((b) => b.id === config.listenInterface);
		return match?.ip ?? '';
	});

	const activeLabel = $derived(runtime.activeTag || runtime.defaultTag);

	const noTunnels = $derived(outbounds.length <= 1);

	let toggling = $state(false);

	function handleSwitched() {
		deviceProxyRuntime.invalidate();
	}

	function handleSaved(_saved: DeviceProxyConfig) {
		deviceProxyConfig.invalidate();
		deviceProxyRuntime.invalidate();
		settingsDrawerOpen = false;
	}

	async function handleToggleEnabled() {
		if (!config || toggling) return;
		toggling = true;
		const next = !config.enabled;
		try {
			await api.saveDeviceProxyConfig({ ...config, enabled: next });
			deviceProxyConfig.invalidate();
			deviceProxyRuntime.invalidate();
			notifications.success(next ? 'Прокси включён' : 'Прокси выключен');
		} catch (e) {
			notifications.error(`Не удалось переключить: ${(e as Error).message}`);
		} finally {
			toggling = false;
		}
	}
</script>

{#if missingTag}
	<div class="banner banner-error">
		Прокси отключён: выбранный туннель "{missingTag}" был удалён. Выберите другой и включите заново.
	</div>
{/if}

{#if noTunnels && !missingTag}
	<div class="banner banner-info">
		Добавьте хотя бы один туннель в разделе <a href="/tunnels">Туннели</a>, чтобы направлять трафик через VPN.
	</div>
{/if}

{#if configSnap.status === 'loading'}
	<p>Загрузка…</p>
{:else if config}
	<DeviceProxyStatRow
		{config}
		{runtime}
		{bridgeLabel}
		{activeLabel}
		{toggling}
		onToggleEnabled={handleToggleEnabled}
	/>

	{#if config.enabled}
		<div class="dashboard-grid">
			<div class="dashboard-left">
				<ActiveTunnelCard
					{outbounds}
					{runtime}
					onSwitched={handleSwitched}
				/>
			</div>
			<div class="dashboard-right">
				<DeviceProxyClientInfoCard
					{config}
					{resolvedListenIP}
					{bridgeLabel}
					onOpenSettings={() => (settingsDrawerOpen = true)}
				/>
			</div>
		</div>
	{:else}
		<div class="banner banner-info disabled-banner">
			<span>Прокси выключен.</span>
			<button type="button" class="link-btn" onclick={() => (settingsDrawerOpen = true)}>
				Открыть настройки
			</button>
		</div>
	{/if}

	<SideDrawer
		open={settingsDrawerOpen}
		onClose={() => (settingsDrawerOpen = false)}
		title="Настройки прокси"
		width={560}
	>
		<SettingsCard
			{config}
			{outbounds}
			{bridgeInterfaces}
			onSaved={handleSaved}
			onCancel={() => (settingsDrawerOpen = false)}
		/>
	</SideDrawer>
{/if}

<style>
	.banner {
		padding: 0.75rem 1rem;
		border-radius: var(--radius);
		margin-bottom: 0.75rem;
		font-size: 0.875rem;
	}
	.banner-error {
		border: 1px solid var(--color-error);
		background: rgba(247, 118, 142, 0.08);
		color: var(--color-error);
	}
	.banner-info {
		border: 1px solid var(--color-border);
		background: var(--color-bg-secondary);
		color: var(--color-text-secondary);
	}

	.disabled-banner {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
	}

	.link-btn {
		background: none;
		border: none;
		color: var(--color-accent);
		font-size: inherit;
		font-family: inherit;
		cursor: pointer;
		padding: 0;
		text-decoration: underline;
	}

	.dashboard-grid {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 1rem;
		align-items: start;
	}

	.dashboard-left,
	.dashboard-right {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
	}

	@media (max-width: 900px) {
		.dashboard-grid {
			grid-template-columns: 1fr;
		}
	}
</style>
