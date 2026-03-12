<script lang="ts">
	import type { DnsRoute, DnsRouteTunnelInfo } from '$lib/types';
	import DnsRouteCardHeader from './DnsRouteCardHeader.svelte';
	import DnsRouteCardRoutes from './DnsRouteCardRoutes.svelte';

	interface Props {
		route: DnsRoute;
		tunnels?: DnsRouteTunnelInfo[];
		ontoggle: (enabled: boolean) => void;
		onedit: () => void;
		ondelete: () => void;
		onrefresh: () => void;
		toggleLoading?: boolean;
	}

	let {
		route,
		tunnels = [],
		ontoggle,
		onedit,
		ondelete,
		onrefresh,
		toggleLoading = false
	}: Props = $props();
</script>

<div class="card" class:enabled={route.enabled}>
	<DnsRouteCardHeader {route} {ontoggle} {toggleLoading} />
	<DnsRouteCardRoutes routes={route.routes} {tunnels} />
	<div class="actions">
		<button class="btn btn-ghost" onclick={() => onedit()}>Изменить</button>
		<button class="btn btn-ghost" onclick={() => onrefresh()} title="Обновить подписки">
			Обновить
		</button>
		<button class="btn btn-ghost btn-danger-ghost" onclick={() => ondelete()}>Удалить</button>
	</div>
</div>

<style>
	.card {
		transition: border-color 0.2s ease;
	}

	.card.enabled {
		border-color: var(--success);
	}


	.actions {
		display: flex;
		gap: 0.5rem;
		padding-top: 0.75rem;
		border-top: 1px solid var(--border);
	}

	.btn-ghost {
		background: transparent;
		color: var(--text-secondary);
		border: none;
		font-size: 0.8125rem;
		cursor: pointer;
		padding: 0.25rem 0.5rem;
		border-radius: 4px;
	}

	.btn-ghost:hover {
		background: var(--bg-hover);
		color: var(--text-primary);
	}

	.btn-danger-ghost {
		color: var(--text-muted);
	}

	.btn-danger-ghost:hover {
		background: rgba(239, 68, 68, 0.15);
		color: var(--error);
	}
</style>
