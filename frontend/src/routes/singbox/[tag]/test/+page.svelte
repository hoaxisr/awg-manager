<script lang="ts">
	import { m } from '$lib/i18n';
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { api } from '$lib/api/client';
	import type { SingboxTunnel } from '$lib/types';
	import TunnelDiagnosticsPanel from '$lib/components/testing/TunnelDiagnosticsPanel.svelte';

	let tunnelTag = $derived($page.params.tag as string);
	let tunnel: SingboxTunnel | null = $state(null);
	let tunnelLoaded = $state(false);

	let displayName = $derived.by(() => tunnel ? tunnel.tag : tunnelTag);
	let unavailableReason = $derived.by(() => {
		if (!tunnelLoaded) return undefined;
		if (tunnel === null) return m.singbox_test_not_found();
		if (!tunnel.kernelInterface) return m.singbox_no_kernel_iface();
		return undefined;
	});

	onMount(async () => {
		try {
			const all = await api.singboxListTunnels();
			tunnel = all.find((t) => t.tag === tunnelTag) ?? null;
		} catch {
			tunnel = null;
		} finally {
			tunnelLoaded = true;
		}
	});
</script>

<TunnelDiagnosticsPanel
	kind="singbox"
	targetId={tunnelTag}
	{displayName}
	backHref="/?tab=singbox"
	backLabel={m.tunnels_back_to_list()}
	subject="tunnel"
	iface={tunnel?.kernelInterface}
	loading={!tunnelLoaded}
	{unavailableReason}
/>
