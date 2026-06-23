<script lang="ts">
	import type { StaticRouteList, RoutingTunnel } from '$lib/types';
	import { Toggle, Badge } from '$lib/components/ui';
	import RoutingTargetBadges from '$lib/components/routing/RoutingTargetBadges.svelte';
	import { ServiceIcon } from '$lib/components/dnsroutes';
	import { parseSubnetComment } from '$lib/utils/cidr';
	import { SquarePen, Trash2 } from 'lucide-svelte';

	interface Props {
		route: StaticRouteList;
		tunnels?: RoutingTunnel[];
		ontoggle: (enabled: boolean) => void;
		onedit: () => void;
		ondelete: () => void;
		toggleLoading?: boolean;
		selectable?: boolean;
		selected?: boolean;
		onselect?: () => void;
		onicon?: () => void;
	}

	let {
		route,
		tunnels = [],
		ontoggle,
		onedit,
		ondelete,
		toggleLoading = false,
		selectable = false,
		selected = false,
		onselect,
		onicon
	}: Props = $props();

	let subnetCount = $derived(route.subnets?.length ?? 0);

	let commentTags = $derived.by(() => {
		const comments = new Set<string>();
		for (const s of route.subnets ?? []) {
			const { comment } = parseSubnetComment(s);
			if (comment) comments.add(comment);
		}
		return [...comments];
	});

	let routeTarget = $derived.by(() => {
		if (!route.tunnelID) return '';
		const tuns = tunnels ?? [];
		if (tuns.length > 0) {
			const found = tuns.find(t => t.id === route.tunnelID);
			if (found) return found.name;
		}
		return route.tunnelID;
	});

	let isOrphan = $derived(!route.tunnelID);
</script>

<div
	class="dns-card"
	class:enabled={route.enabled}
	class:orphan={isOrphan}
	class:selected={selectable && selected}
>
	<div class="card-main">
		{#if selectable}
			<input
				type="checkbox"
				class="select-check"
				checked={selected}
				onchange={() => onselect?.()}
			/>
		{/if}
		{#if onicon && !selectable}
			<button
				class="icon-btn"
				type="button"
				onclick={() => onicon()}
				aria-label="Сменить иконку"
				title="Сменить иконку"
			>
				<ServiceIcon name={route.name} iconUrl={route.iconUrl} size={36} />
			</button>
		{:else}
			<ServiceIcon name={route.name} iconUrl={route.iconUrl} size={36} />
		{/if}
		<div class="card-info">
			<div class="card-title">
				<span
					class="led"
					class:led-green={route.enabled}
					class:led-gray={!route.enabled}
				></span>
				<h3 title={route.name}>{route.name}</h3>
			</div>
			{#if subnetCount > 0}
				<span class="card-stat">
					{subnetCount} подсетей
					{#if commentTags.length > 0}
						<span class="comment-sep">&middot;</span>
						<span class="comment-tags">
							{commentTags.slice(0, 3).join(', ')}
							{#if commentTags.length > 3}
								<span class="comment-more">+{commentTags.length - 3} ещё</span>
							{/if}
						</span>
					{/if}
				</span>
			{/if}
			{#if routeTarget}
				<div class="card-route">
					<RoutingTargetBadges labels={[routeTarget]} overflowNoun="туннелей" />
					{#if route.fallback === 'reject'}
						<Badge variant="error" uppercase size="xs">Kill Switch</Badge>
					{/if}
				</div>
			{:else if isOrphan}
				<div class="card-route">
					<Badge
						variant="warning"
						uppercase
						size="xs"
						title="Туннель, к которому был привязан этот список, удалён. Нажмите «Изменить» и выберите новый туннель."
					>
						Без туннеля
					</Badge>
				</div>
			{/if}
		</div>
	</div>
	<div class="card-actions">
		<Toggle
			checked={route.enabled}
			onchange={(checked) => ontoggle(checked)}
			loading={toggleLoading}
			disabled={isOrphan}
			size="sm"
		/>
		<div class="action-row">
			<button
				type="button"
				class="route-action-btn"
				title={`Изменить IP-маршрут «${route.name}»`}
				onclick={() => onedit()}
			>
				<SquarePen size={15} aria-hidden="true" />
			</button>
			<button
				type="button"
				class="route-action-btn danger"
				title={`Удалить IP-маршрут «${route.name}»`}
				onclick={() => ondelete()}
			>
				<Trash2 size={15} aria-hidden="true" />
			</button>
		</div>
	</div>
</div>

<style>
	.dns-card {
		display: flex;
		justify-content: space-between;
		border-radius: 8px;
		padding: 14px;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		transition: border-color 0.2s;
	}

	.dns-card:hover {
		border-color: var(--border-hover);
	}

	.dns-card:not(.enabled) {
		opacity: 0.4;
	}

	.dns-card.selected {
		border-color: var(--accent);
	}

	.card-main {
		display: flex;
		gap: 10px;
		min-width: 0;
	}

	.card-info {
		display: flex;
		flex-direction: column;
		gap: 1px;
		min-width: 0;
	}

	.card-title {
		display: flex;
		align-items: center;
		gap: 6px;
		min-width: 0;
	}

	.card-title h3 {
		font-size: 0.875rem;
		font-weight: 600;
		margin: 0;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		min-width: 0;
	}

	.card-stat {
		font-size: 0.6875rem;
		color: var(--text-muted);
	}

	.card-actions {
		display: flex;
		flex-direction: column;
		align-items: flex-end;
		gap: 8px;
		flex-shrink: 0;
		margin-left: 8px;
		align-self: stretch;
	}

	.action-row {
		display: flex;
		gap: 4px;
		align-items: center;
		margin-top: auto;
	}

	.led {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		flex-shrink: 0;
	}

	.led-green {
		background: var(--success);
		box-shadow: 0 0 6px var(--success);
	}

	.led-gray {
		background: var(--text-muted);
	}

	.select-check {
		accent-color: var(--accent);
		width: 16px;
		height: 16px;
		cursor: pointer;
		flex-shrink: 0;
		margin-top: 10px;
	}

	.icon-btn {
		padding: 0;
		background: none;
		border: 1px solid transparent;
		border-radius: 7px;
		cursor: pointer;
		transition: border-color 0.15s;
		display: flex;
		align-items: center;
		justify-content: center;
		flex-shrink: 0;
	}

	.icon-btn:hover {
		border-color: var(--border-hover);
	}

	.icon-btn:focus-visible {
		outline: 2px solid var(--accent);
		outline-offset: 2px;
	}

	.comment-sep {
		margin: 0 4px;
		color: var(--text-muted);
	}

	.comment-tags {
		color: var(--text-secondary);
		font-size: 0.625rem;
	}

	.comment-more {
		color: var(--accent);
		font-weight: 500;
	}

	.dns-card.orphan {
		opacity: 0.7;
		border: 1px dashed var(--warn, #d08770);
	}

</style>
