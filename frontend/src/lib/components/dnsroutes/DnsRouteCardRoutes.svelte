<script lang="ts">
	import type { DnsRouteTarget, DnsRouteTunnelInfo } from '$lib/types';

	interface Props {
		routes: DnsRouteTarget[];
		tunnels?: DnsRouteTunnelInfo[];
	}

	let { routes: rawRoutes, tunnels: rawTunnels }: Props = $props();

	// Go nil slices serialize as JSON null — normalize to empty arrays
	let routes = $derived(rawRoutes ?? []);
	let tunnels = $derived(rawTunnels ?? []);

	function tunnelDisplayName(target: DnsRouteTarget): string {
		if (tunnels.length > 0) {
			const found = tunnels.find((tun) => tun.id === target.tunnelId);
			if (found) return found.name;
		}
		return target.tunnelId;
	}

	let lastRoute = $derived(routes.length > 0 ? routes[routes.length - 1] : null);

	let fallbackLabel = $derived.by(() => {
		const fb = lastRoute?.fallback;
		if (!fb) return '';
		if (fb === 'auto') return 'auto';
		if (fb === 'reject') return 'reject';
		return '';
	});
</script>

{#if routes.length > 0}
	<div class="route-chain">
		{#each routes as target}
			<span class="route-arrow">&rarr;</span>
			<span class="route-tunnel">{tunnelDisplayName(target)}</span>
		{/each}
		{#if fallbackLabel}
			<span class="route-arrow">&rarr;</span>
			<span class="route-fallback">{fallbackLabel}</span>
		{/if}
	</div>
{:else}
	<div class="route-chain">
		<span class="route-empty">Маршрут не настроен</span>
	</div>
{/if}

<style>
	.route-chain {
		display: flex;
		align-items: center;
		gap: 0.375rem;
		flex-wrap: wrap;
		font-size: 0.8125rem;
	}

	.route-arrow {
		color: var(--text-muted);
	}

	.route-tunnel {
		color: var(--accent);
		font-weight: 500;
	}

	.route-fallback {
		color: var(--text-muted);
		font-style: italic;
	}

	.route-empty {
		color: var(--text-muted);
		font-style: italic;
	}
</style>
