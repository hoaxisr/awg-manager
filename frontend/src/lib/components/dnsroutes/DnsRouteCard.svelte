<script lang="ts">
	import type { DnsRoute, RoutingTunnel } from '$lib/types';
	import { Toggle, Badge } from '$lib/components/ui';
	import RoutingTargetBadges from '$lib/components/routing/RoutingTargetBadges.svelte';
	import { ServiceIcon } from '$lib/components/dnsroutes';
	import { SquarePen, Trash2, RefreshCw } from 'lucide-svelte';

	interface Props {
		route: DnsRoute;
		tunnels?: RoutingTunnel[];
		ontoggle: (enabled: boolean) => void;
		onedit: () => void;
		ondelete: () => void;
		onrefresh: () => void;
		toggleLoading?: boolean;
		selectable?: boolean;
		selected?: boolean;
		onselect?: () => void;
		onicon?: () => void;
		downloadRouteLabel?: string;
	}

	let {
		route,
		tunnels = [],
		ontoggle,
		onedit,
		ondelete,
		onrefresh,
		toggleLoading = false,
		selectable = false,
		selected = false,
		onselect,
		onicon,
		downloadRouteLabel = ''
	}: Props = $props();

	// Post-split data stores CIDRs in route.subnets; legacy lists created
	// before commit a65b76f4 (2026-04-15) may still have CIDRs mixed into
	// route.domains until the next save re-runs splitDomainsAndSubnets.
	let cidrCount = $derived(
		(route.subnets?.length ?? 0) +
		(route.domains ?? []).filter(d => d.includes('/')).length
	);
	let domainCount = $derived((route.domains ?? []).filter(d => !d.includes('/')).length);
	let subCount = $derived(route.subscriptions?.length ?? 0);
	let manualCount = $derived(route.manualDomains?.length ?? 0);

	let dedupReport = $derived(route.lastDedupeReport);
	let hasDedups = $derived(dedupReport && dedupReport.totalRemoved > 0);

	let sourceSummary = $derived.by(() => {
		if (subCount > 0 && manualCount > 0) return `${subCount} листов + ${manualCount} вручную`;
		if (subCount > 0) return `${subCount} листов`;
		if (manualCount > 0) return 'все вручную';
		return '';
	});

	let routeTargets = $derived.by(() => {
		const tuns = tunnels;
		return (route.routes ?? []).map((target) => {
			const found = tuns.find((t) => t.id === target.tunnelId);
			if (found) return found.name;
			return target.interface || target.tunnelId;
		});
	});

	// Orphan = list whose bindings all pointed to a tunnel that got
	// deleted. Domains / subscriptions are preserved so the user can
	// rebind them to another tunnel via the Edit modal.
	let isOrphan = $derived((route.routes?.length ?? 0) === 0);
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
			{#if domainCount > 0}
				<span class="card-stat">{domainCount} доменов</span>
			{/if}
			{#if cidrCount > 0}
				<span class="card-stat">{cidrCount} CIDR</span>
			{/if}
			{#if sourceSummary}
				<span class="card-source">{sourceSummary}</span>
			{/if}
			{#if subCount > 0 && downloadRouteLabel}
				<span class="card-download-route" title={downloadRouteLabel}>
					Обновление через {downloadRouteLabel}
				</span>
			{/if}
			{#if hasDedups}
				<span class="card-dedup" title={dedupReport?.items?.map(
					i => `${i.domain} — ${i.reason === 'exact' ? 'дубль' : 'покрыт'} ${i.coveredBy} (${i.listName || i.listId})`
				).join('\n') ?? ''}>
					{dedupReport?.totalRemoved} убрано
				</span>
			{/if}
			{#if routeTargets.length > 0}
				<div class="card-route">
					<RoutingTargetBadges labels={routeTargets} overflowNoun="туннелей" />
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
				title={`Изменить DNS-маршрут «${route.name}»`}
				onclick={() => onedit()}
			>
				<SquarePen size={15} />
			</button>
			<button
				type="button"
				class="route-action-btn success"
				title={downloadRouteLabel
					? `Обновить подписки DNS-маршрута «${route.name}» через ${downloadRouteLabel}`
					: `Обновить подписки DNS-маршрута «${route.name}»`}
				onclick={() => onrefresh()}
			>
				<RefreshCw size={15} />
			</button>
			<button
				type="button"
				class="route-action-btn danger"
				title={`Удалить DNS-маршрут «${route.name}»`}
				onclick={() => ondelete()}
			>
				<Trash2 size={15} />
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

	.dns-card.orphan {
		opacity: 0.7;
		border: 1px dashed var(--warn, #d08770);
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
		color: var(--text-primary);
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

	.card-source {
		font-size: 0.625rem;
		color: var(--text-secondary);
	}

	.card-download-route {
		font-size: 0.625rem;
		color: var(--text-muted);
		max-width: 100%;
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
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

	.card-dedup {
		font-size: 0.625rem;
		color: var(--warning, #f59e0b);
		cursor: help;
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

	:global(html[data-theme-preset='neo']) .card-source,
	:global(html[data-theme-preset='neo']) .card-route {
		color: var(--text-primary);
	}
</style>
