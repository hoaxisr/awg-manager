<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { api } from '$lib/api/client';
	import type { AWGTunnel } from '$lib/types';
	import TunnelDiagnosticsPanel from '$lib/components/testing/TunnelDiagnosticsPanel.svelte';

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
	backHref="/awg/tunnels"
	backLabel="К списку туннелей"
	subjectLabel="туннель"
/>
