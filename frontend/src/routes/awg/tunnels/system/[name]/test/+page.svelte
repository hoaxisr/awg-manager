<script lang="ts">
	import { page } from '$app/stores';
	import TunnelDiagnosticsPanel from '$lib/components/testing/TunnelDiagnosticsPanel.svelte';

	let tunnelName = $derived($page.params.name as string);
</script>

<TunnelDiagnosticsPanel
	kind="system"
	mode="page"
	targetId={tunnelName}
	displayName={tunnelName}
	backHref="/awg/tunnels"
	backLabel="К списку туннелей"
	subjectLabel="туннель"
/>
