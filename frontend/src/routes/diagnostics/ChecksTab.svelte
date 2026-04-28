<script lang="ts">
	import { HealthRailRunChecks, HealthRailChecks, HealthRailDnsCheck } from '$lib/components/diagnostics';
	import type { TunnelListItem } from '$lib/types';

	interface Props {
		tunnels: TunnelListItem[];
	}

	let { tunnels }: Props = $props();
</script>

<div class="checks-grid">
	<aside class="checks-left">
		<HealthRailRunChecks {tunnels} />
	</aside>
	<main class="checks-right">
		<HealthRailChecks />
		<HealthRailDnsCheck />
	</main>
</div>

<style>
	.checks-grid {
		display: grid;
		grid-template-columns: 360px 1fr;
		gap: 1rem;
		align-items: start;
	}

	.checks-left,
	.checks-right {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
	}

	@media (max-width: 900px) {
		.checks-grid {
			grid-template-columns: 1fr;
		}
	}
</style>
