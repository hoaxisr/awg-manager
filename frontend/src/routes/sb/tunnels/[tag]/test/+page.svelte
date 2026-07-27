<script lang="ts">
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
		if (tunnel === null) return 'Туннель не найден или недоступен для расширенного тестирования.';
		if (!tunnel.kernelInterface) return 'У этого sing-box туннеля нет kernel interface, расширенные тесты недоступны.';
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
	backHref="/sb/tunnels"
	backLabel="К списку туннелей"
	subjectLabel="туннель"
	iface={tunnel?.kernelInterface}
	loading={!tunnelLoaded}
	{unavailableReason}
/>
