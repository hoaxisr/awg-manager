<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { deviceProxyConfig, deviceProxyOutbounds, deviceProxyMissingTarget } from '$lib/stores/deviceproxy';
	import ActiveOutboundCard from './ActiveOutboundCard.svelte';
	import InboundSettingsCard from './InboundSettingsCard.svelte';
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
	let choices = $state<ListenChoices | null>(null);

	onMount(() => {
		unsubConfig = deviceProxyConfig.subscribe(() => {});
		unsubOutbounds = deviceProxyOutbounds.subscribe(() => {});
		api.getDeviceProxyListenChoices().then((v) => { choices = v; }).catch(() => {});
	});
	onDestroy(() => {
		unsubConfig?.();
		unsubOutbounds?.();
	});

	let configSnap = $derived($deviceProxyConfig);
	let outboundsSnap = $derived($deviceProxyOutbounds);

	let config = $derived<DeviceProxyConfig | null>(configSnap.data ?? null);
	let outbounds = $derived(outboundsSnap.data ?? []);

	let missingTag = $derived($deviceProxyMissingTarget);
	let singboxRunning = $derived(choices?.singboxRunning ?? false);
	let bridgeInterfaces = $derived(
		(choices?.bridges ?? [{ id: 'Bridge0', label: 'Bridge0' }]).map((b) => ({
			id: b.id,
			label: b.label,
		})),
	);
	let resolvedListenIP = $derived.by(() => {
		if (!config || !choices) return '';
		if (config.listenAll) return choices.lanIP || '';
		const match = choices.bridges.find((b) => b.id === config.listenInterface);
		return match?.ip ?? '';
	});

	function handleOutboundChanged(_tag: string) {
		deviceProxyConfig.invalidate();
	}

	function handleSaved(_saved: DeviceProxyConfig) {
		deviceProxyConfig.invalidate();
	}
</script>

{#if missingTag}
	<div class="missing-banner">
		Прокси отключён: выбранный outbound "{missingTag}" был удалён. Выберите другой и включите заново.
	</div>
{/if}

{#if configSnap.status === 'loading'}
	<p>Загрузка…</p>
{:else if config}
	<div class="settings-stack">
		<ActiveOutboundCard
			outbounds={outbounds}
			activeTag={config.selectedOutbound || 'direct'}
			{singboxRunning}
			onChanged={handleOutboundChanged}
		/>
		<InboundSettingsCard
			{config}
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
	.missing-banner {
		padding: 12px;
		margin-bottom: 12px;
		border: 1px solid var(--error, #ef4444);
		border-radius: 8px;
		background: rgba(239, 68, 68, 0.08);
		color: var(--error, #ef4444);
	}
</style>
