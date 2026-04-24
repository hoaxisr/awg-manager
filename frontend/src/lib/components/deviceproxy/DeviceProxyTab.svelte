<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { deviceProxyConfig, deviceProxyOutbounds } from '$lib/stores/deviceproxy';
	import ActiveOutboundCard from './ActiveOutboundCard.svelte';
	import InboundSettingsCard from './InboundSettingsCard.svelte';
	import ConnectionInfoCard from './ConnectionInfoCard.svelte';
	import type { DeviceProxyConfig } from '$lib/types';

	let unsubConfig: (() => void) | null = null;
	let unsubOutbounds: (() => void) | null = null;

	onMount(() => {
		unsubConfig = deviceProxyConfig.subscribe(() => {});
		unsubOutbounds = deviceProxyOutbounds.subscribe(() => {});
	});
	onDestroy(() => {
		unsubConfig?.();
		unsubOutbounds?.();
	});

	let configSnap = $derived($deviceProxyConfig);
	let outboundsSnap = $derived($deviceProxyOutbounds);

	let config = $derived<DeviceProxyConfig | null>(configSnap.data ?? null);
	let outbounds = $derived(outboundsSnap.data ?? []);

	// TODO(task18): thread real systemInfo values.
	let singboxRunning = $state(true);
	let bridgeInterfaces = $state([{ id: 'Bridge0', label: 'Bridge0 (Home)' }]);
	let resolvedListenIP = $derived(config?.listenAll ? 'router-lan-ip' : 'resolved-bridge-ip');

	function handleOutboundChanged(_tag: string) {
		deviceProxyConfig.invalidate();
	}

	function handleSaved(_saved: DeviceProxyConfig) {
		deviceProxyConfig.invalidate();
	}
</script>

{#if configSnap.status === 'loading'}
	<p>Загрузка…</p>
{:else if config}
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
{/if}
