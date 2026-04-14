<script lang="ts">
	import type { DnsRoute, RoutingTunnel } from '$lib/types';
	import { Toggle } from '$lib/components/ui';

	interface Props {
		routes: DnsRoute[];
		tunnels: RoutingTunnel[];
		policyOrder: string[];
		ontoggle: (id: string, enabled: boolean) => void;
		onedit: (route: DnsRoute) => void;
		ondelete: (id: string) => void;
		onrefresh: (id: string) => void;
		onapplyorder: (order: string[]) => void;
		toggleLoadingId?: string | null;
		hydrarouteInstalled?: boolean;
	}

	let {
		routes,
		tunnels = [],
		policyOrder,
		ontoggle,
		onedit,
		ondelete,
		onrefresh,
		onapplyorder,
		toggleLoadingId = null,
		hydrarouteInstalled = false,
	}: Props = $props();

	// Local order state — initialized from policyOrder prop, modified by drag/arrows
	let localOrder = $state<string[]>([]);

	// Sync from prop when it changes externally (e.g. after apply)
	$effect(() => {
		localOrder = [...policyOrder];
	});

	// Sort routes by position in localOrder; unordered go to end
	let sortedRoutes = $derived.by(() => {
		const orderMap = new Map(localOrder.map((name, i) => [name, i]));
		return [...routes].sort((a, b) => {
			const aTarget = getTarget(a);
			const bTarget = getTarget(b);
			const aIdx = orderMap.get(aTarget) ?? 9999;
			const bIdx = orderMap.get(bTarget) ?? 9999;
			return aIdx - bIdx;
		});
	});

	let isDirty = $derived.by(() => {
		if (localOrder.length !== policyOrder.length) return true;
		return localOrder.some((name, i) => name !== policyOrder[i]);
	});

	function getTarget(route: DnsRoute): string {
		if (route.hrRouteMode === 'policy' && route.hrPolicyName) {
			return route.hrPolicyName;
		}
		// Interface mode: use the kernel interface from first route target
		const r = route.routes?.[0];
		return r?.interface || '';
	}

	function getTargetLabel(route: DnsRoute): string {
		return getTarget(route);
	}

	function getModeLabel(route: DnsRoute): string {
		return route.hrRouteMode === 'policy' ? 'policy' : 'interface';
	}

	function moveUp(index: number) {
		if (index <= 0) return;
		const targets = sortedRoutes.map(r => getTarget(r));
		const next = [...targets];
		[next[index - 1], next[index]] = [next[index], next[index - 1]];
		localOrder = next;
	}

	function moveDown(index: number) {
		if (index >= sortedRoutes.length - 1) return;
		const targets = sortedRoutes.map(r => getTarget(r));
		const next = [...targets];
		[next[index], next[index + 1]] = [next[index + 1], next[index]];
		localOrder = next;
	}

	// Drag and drop
	let dragIndex = $state<number | null>(null);
	let dragOverIndex = $state<number | null>(null);

	function handleDragStart(e: DragEvent, index: number) {
		dragIndex = index;
		if (e.dataTransfer) {
			e.dataTransfer.effectAllowed = 'move';
			e.dataTransfer.setData('text/plain', String(index));
		}
	}

	function handleDragOver(e: DragEvent, index: number) {
		e.preventDefault();
		if (e.dataTransfer) e.dataTransfer.dropEffect = 'move';
		dragOverIndex = index;
	}

	function handleDragLeave() {
		dragOverIndex = null;
	}

	function handleDrop(e: DragEvent, dropIndex: number) {
		e.preventDefault();
		dragOverIndex = null;
		if (dragIndex === null || dragIndex === dropIndex) {
			dragIndex = null;
			return;
		}

		const targets = sortedRoutes.map(r => getTarget(r));
		const next = [...targets];
		const [moved] = next.splice(dragIndex, 1);
		next.splice(dropIndex, 0, moved);
		localOrder = next;
		dragIndex = null;
	}

	function handleDragEnd() {
		dragIndex = null;
		dragOverIndex = null;
	}

	function routeTarget(route: DnsRoute): string {
		const routes = route.routes ?? [];
		if (routes.length === 0) return '';
		const first = routes[0];
		const found = tunnels.find(t => t.id === first.tunnelId);
		return found?.name || first.interface || first.tunnelId;
	}
</script>

<div class="hrneo-section">
	<div class="section-title">
		<div class="title-left">
			<span class="title-text">HydraRoute Neo</span>
			<span class="title-count">{routes.length}</span>
		</div>
		{#if isDirty}
			<button class="btn-apply" onclick={() => onapplyorder(localOrder)}>
				✓ Применить порядок
			</button>
		{/if}
	</div>

	{#if sortedRoutes.length === 0}
		<div class="empty-hint">Нет списков HydraRoute Neo</div>
	{:else}
		<div class="route-list">
			{#each sortedRoutes as route, index (route.id)}
				{@const target = getTargetLabel(route)}
				{@const mode = getModeLabel(route)}
				<div
					class="route-item"
					class:disabled={!route.enabled}
					class:drag-over={dragOverIndex === index}
					draggable="true"
					ondragstart={(e) => handleDragStart(e, index)}
					ondragover={(e) => handleDragOver(e, index)}
					ondragleave={handleDragLeave}
					ondrop={(e) => handleDrop(e, index)}
					ondragend={handleDragEnd}
				>
					<span class="drag-handle" title="Перетащите для изменения порядка">⋮⋮</span>
					<div class="arrows">
						<button
							class="arrow-btn"
							disabled={index === 0}
							onclick={() => moveUp(index)}
							title="Вверх"
						>▲</button>
						<button
							class="arrow-btn"
							disabled={index === sortedRoutes.length - 1}
							onclick={() => moveDown(index)}
							title="Вниз"
						>▼</button>
					</div>
					<span class="priority-num">{index + 1}</span>
					<Toggle
						checked={route.enabled}
						onchange={(checked) => ontoggle(route.id, checked)}
						loading={toggleLoadingId === route.id}
						size="sm"
					/>
					<div class="route-info">
						<div class="route-name">{route.name}</div>
						<div class="route-meta">
							{#if (route.domains?.length ?? 0) > 0}
								<span>{route.domains?.length} доменов</span>
								<span class="sep">·</span>
							{/if}
							<span class="target-name">{target}</span>
							<span class="sep">·</span>
							<span class="mode-badge" class:mode-policy={mode === 'policy'} class:mode-iface={mode === 'interface'}>{mode}</span>
						</div>
					</div>
					<button class="edit-btn" title="Изменить" onclick={() => onedit(route)}>✎</button>
				</div>
			{/each}
		</div>
	{/if}
</div>

<style>
	.hrneo-section {
		display: flex;
		flex-direction: column;
		gap: 12px;
	}

	.section-title {
		display: flex;
		align-items: center;
		justify-content: space-between;
	}

	.title-left {
		display: flex;
		align-items: center;
		gap: 8px;
	}

	.title-text {
		font-size: 0.8125rem;
		font-weight: 600;
		text-transform: uppercase;
		letter-spacing: 1px;
		color: var(--accent);
	}

	.title-count {
		background: var(--accent);
		color: white;
		padding: 1px 6px;
		border-radius: 3px;
		font-size: 0.625rem;
	}

	.btn-apply {
		background: var(--success);
		color: white;
		border: none;
		padding: 6px 14px;
		border-radius: 6px;
		cursor: pointer;
		font-size: 0.75rem;
		font-family: inherit;
		display: flex;
		align-items: center;
		gap: 4px;
	}

	.btn-apply:hover {
		filter: brightness(1.1);
	}

	.empty-hint {
		color: var(--text-muted);
		font-size: 0.8125rem;
		text-align: center;
		padding: 1rem;
	}

	.route-list {
		display: flex;
		flex-direction: column;
		gap: 6px;
	}

	.route-item {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 10px 12px;
		background: var(--bg-secondary);
		border-radius: 8px;
		border-left: 3px solid var(--accent);
		cursor: grab;
		transition: opacity 0.15s;
	}

	.route-item.disabled {
		opacity: 0.5;
	}

	.route-item.drag-over {
		border-top: 2px solid var(--accent);
	}

	.drag-handle {
		color: var(--accent);
		font-size: 14px;
		cursor: grab;
		user-select: none;
		letter-spacing: 1px;
		flex-shrink: 0;
	}

	.arrows {
		display: flex;
		flex-direction: column;
		gap: 0;
	}

	.arrow-btn {
		background: none;
		border: none;
		color: var(--text-muted);
		font-size: 9px;
		cursor: pointer;
		padding: 0;
		line-height: 1;
	}

	.arrow-btn:hover:not(:disabled) {
		color: var(--accent);
	}

	.arrow-btn:disabled {
		opacity: 0.3;
		cursor: default;
	}

	.priority-num {
		color: var(--accent);
		font-weight: 700;
		font-size: 0.9375rem;
		width: 20px;
		text-align: center;
		flex-shrink: 0;
	}

	.route-info {
		flex: 1;
		min-width: 0;
	}

	.route-name {
		font-weight: 600;
		font-size: 0.875rem;
		color: var(--text-primary);
	}

	.route-meta {
		font-size: 0.75rem;
		color: var(--text-muted);
		display: flex;
		align-items: center;
		gap: 6px;
		margin-top: 2px;
		flex-wrap: wrap;
	}

	.sep {
		color: var(--border);
	}

	.target-name {
		color: var(--accent);
	}

	.mode-badge {
		padding: 0 5px;
		border-radius: 3px;
		font-size: 0.625rem;
	}

	.mode-policy {
		background: rgba(99, 102, 241, 0.2);
		color: var(--accent);
	}

	.mode-iface {
		background: rgba(56, 189, 248, 0.15);
		color: #7dd3fc;
	}

	.edit-btn {
		background: none;
		border: 1px solid var(--border);
		color: var(--text-muted);
		padding: 4px 8px;
		border-radius: 4px;
		cursor: pointer;
		font-size: 0.6875rem;
		flex-shrink: 0;
		font-family: inherit;
	}

	.edit-btn:hover {
		color: var(--accent);
		border-color: var(--accent);
	}
</style>
