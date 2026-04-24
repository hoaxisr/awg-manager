<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import {
		deviceProxyConfig,
		deviceProxyOutbounds,
		deviceProxyRuntime,
		deviceProxyMissingTarget,
	} from '$lib/stores/deviceproxy';
	import ActiveTunnelCard from './ActiveTunnelCard.svelte';
	import SettingsCard from './SettingsCard.svelte';
	import ConnectionInfoCard from './ConnectionInfoCard.svelte';
	import { api } from '$lib/api/client';
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

	let configSnap = $derived($deviceProxyConfig);
	let outboundsSnap = $derived($deviceProxyOutbounds);
	let runtimeSnap = $derived($deviceProxyRuntime);

	let config = $derived<DeviceProxyConfig | null>(configSnap.data ?? null);
	let outbounds = $derived(outboundsSnap.data ?? []);
	let runtime = $derived(runtimeSnap.data ?? { alive: false, activeTag: '', defaultTag: '' });

	let missingTag = $derived($deviceProxyMissingTarget);

	let bridgeInterfaces = $derived(
		(choices?.bridges ?? [{ id: 'Bridge0', label: 'Bridge0' }]).map((b) => ({ id: b.id, label: b.label })),
	);
	let resolvedListenIP = $derived.by(() => {
		if (!config || !choices) return '';
		if (config.listenAll) return choices.lanIP || '';
		const match = choices.bridges.find((b) => b.id === config.listenInterface);
		return match?.ip ?? '';
	});

	let noTunnels = $derived(outbounds.length <= 1);

	function handleSwitched() {
		deviceProxyRuntime.invalidate();
	}

	function handleSaved(_saved: DeviceProxyConfig) {
		deviceProxyConfig.invalidate();
		deviceProxyRuntime.invalidate();
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
	<div class="settings-stack">
		{#if config.enabled}
			<ActiveTunnelCard
				{outbounds}
				{runtime}
				onSwitched={handleSwitched}
			/>
		{/if}
		<SettingsCard
			{config}
			{outbounds}
			{bridgeInterfaces}
			onSaved={handleSaved}
		/>
		<ConnectionInfoCard
			{config}
			{resolvedListenIP}
		/>
	</div>
{/if}

<style>
	.banner {
		padding: 0.75rem 1rem;
		border-radius: 8px;
		margin-bottom: 0.75rem;
		font-size: 0.875rem;
	}
	.banner-error {
		border: 1px solid var(--error);
		background: rgba(247, 118, 142, 0.08);
		color: var(--error);
	}
	.banner-info {
		border: 1px solid var(--border);
		background: var(--bg-tertiary);
		color: var(--text-secondary);
	}
</style>
