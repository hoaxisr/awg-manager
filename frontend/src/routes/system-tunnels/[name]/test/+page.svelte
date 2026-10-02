<script lang="ts">
	import { page } from '$app/stores';
	import TunnelDiagnosticsPanel from '$lib/components/testing/TunnelDiagnosticsPanel.svelte';
	import { m } from '$lib/i18n';

	let tunnelName = $derived($page.params.name as string);
</script>

<TunnelDiagnosticsPanel
	kind="system"
	mode="page"
	targetId={tunnelName}
	displayName={tunnelName}
	backHref="/"
	backLabel={m.tunnels_back_to_list()}
	subject="tunnel"
/>
