<script lang="ts">
	import { m } from '$lib/i18n';
	import type { FircTunnel } from '$lib/types/firc';
	import FircResourceCard from './FircResourceCard.svelte';

	interface Props {
		tunnels: FircTunnel[];
		searchActive: boolean;
		isSubscriptionTunnel: (tun: FircTunnel) => boolean;
	}

	let { tunnels, searchActive, isSubscriptionTunnel }: Props = $props();
</script>

<div class="tunnel-table-wrap">
	<table class="tunnel-data-table singbox-tunnel-table">
		<colgroup>
			<col class="col-delay" />
			<col class="col-name" />
			<col class="col-protocol" />
			<col class="col-run" />
			<col class="col-traffic" />
			<col class="col-ping" />
			<col class="col-actions" />
		</colgroup>
		<thead>
			<tr>
				<th>Delay</th>
				<th>{m.tunnels_awg_col_tunnel()}</th>
				<th>{m.singbox_tabs_col_protocol()}</th>
				<th>{m.singbox_tabs_col_process()}</th>
				<th>{m.tunnels_awg_col_traffic()}</th>
				<th>Ping</th>
				<th class="col-actions">{m.tunnels_awg_col_actions()}</th>
			</tr>
		</thead>
		<tbody>
			{#each tunnels as tun, i (tun.id)}
				<FircResourceCard
					kind={isSubscriptionTunnel(tun) ? 'subscription' : 'proxy'}
					tunnel={tun}
					layout="list"
					renderMode="table"
					autoDelayCheckNonce={1}
					autoDelayCheckDelayMs={i * 180}
				/>
			{/each}
			{#if tunnels.length === 0 && searchActive}
				<tr class="tunnel-empty-row">
					<td colspan="7">{m.firc_search_empty()}</td>
				</tr>
			{/if}
		</tbody>
	</table>
</div>
