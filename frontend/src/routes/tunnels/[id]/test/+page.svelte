<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { api } from '$lib/api/client';
	import type { AWGTunnel } from '$lib/types';
	import TunnelDiagnosticsPanel from '$lib/components/testing/TunnelDiagnosticsPanel.svelte';
	import { m } from '$lib/i18n';

	let tunnelId = $derived($page.params.id as string);
	let tunnel: AWGTunnel | null = $state(null);
	let displayName = $derived.by(() => {
		if (tunnel) return tunnel.name;
		return tunnelId;
	});

	onMount(async () => {
		try {
			tunnel = await api.getTunnel(tunnelId);
		} catch {
			// Fallback to tunnelId if fetch fails.
		}
	});
</script>

<TunnelDiagnosticsPanel
	kind="awg"
	targetId={tunnelId}
	{displayName}
	backHref="/"
	backLabel={m.tunnels_back_to_list()}
	subject="tunnel"
/>
