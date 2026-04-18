<script lang="ts">
	import type { StaticRouteList, RoutingTunnel } from '$lib/types';
	import { Toggle } from '$lib/components/ui';
	import { ServiceIcon } from '$lib/components/dnsroutes';
	import { parseSubnetComment } from '$lib/utils/cidr';

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
		onselect
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
		<ServiceIcon name={route.name} size={36} />
		<div class="card-info">
			<div class="card-title">
				<span
					class="led"
					class:led-green={route.enabled}
					class:led-gray={!route.enabled}
				></span>
				<h3>{route.name}</h3>
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
					<span>&rarr;</span> <code>{routeTarget}</code>
				{#if route.fallback === 'reject'}
					<span class="badge-killswitch">Kill Switch</span>
				{/if}
				</div>
			{:else if isOrphan}
				<div class="card-route">
					<span class="badge-orphan" title="Туннель, к которому был привязан этот список, удалён. Нажмите «Изменить» и выберите новый туннель.">Без туннеля</span>
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
		<button class="action-btn" title="Изменить" onclick={() => onedit()}>
			<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
				<path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/>
				<path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/>
			</svg>
		</button>
		<button class="action-btn danger" title="Удалить" onclick={() => ondelete()}>
			<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
				<polyline points="3 6 5 6 21 6"/>
				<path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/>
			</svg>
		</button>
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

	.dns-card.enabled {
		border: 2px solid var(--success);
	}

	.dns-card:not(.enabled) {
		opacity: 0.5;
	}

	.dns-card.selected {
		border-color: var(--accent);
	}

	.card-main {
		display: flex;
		align-items: flex-start;
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
	}

	.card-title h3 {
		font-size: 0.875rem;
		font-weight: 600;
		margin: 0;
	}

	.card-stat {
		font-size: 0.6875rem;
		color: var(--text-muted);
	}

	.card-route {
		font-size: 0.6875rem;
		color: var(--border-hover);
		margin-top: 3px;
	}

	.card-route code {
		background: var(--bg-hover);
		padding: 1px 6px;
		border-radius: 3px;
		font-size: 0.625rem;
		font-family: monospace;
	}

	.card-actions {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 6px;
		flex-shrink: 0;
		margin-left: 8px;
	}

	.action-btn {
		display: flex;
		padding: 2px;
		background: none;
		border: none;
		color: var(--border-hover);
		cursor: pointer;
		border-radius: 4px;
		transition: color 0.15s;
	}

	.action-btn:hover {
		color: var(--accent);
	}

	.action-btn.danger:hover {
		color: var(--error);
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

	.badge-orphan {
		display: inline-block;
		font-size: 0.625rem;
		font-weight: 600;
		color: var(--warn, #d08770);
		background: color-mix(in srgb, var(--warn, #d08770) 15%, transparent);
		padding: 2px 6px;
		border-radius: 3px;
		text-transform: uppercase;
		letter-spacing: 0.03em;
	}

	.badge-killswitch {
		display: inline-block;
		font-size: 0.5625rem;
		font-weight: 600;
		color: var(--error);
		background: color-mix(in srgb, var(--error) 15%, transparent);
		padding: 1px 5px;
		border-radius: 3px;
		margin-left: 6px;
		vertical-align: middle;
		text-transform: uppercase;
		letter-spacing: 0.03em;
	}
</style>
