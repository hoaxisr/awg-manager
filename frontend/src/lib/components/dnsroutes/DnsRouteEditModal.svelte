<script lang="ts">
	import type { DnsRoute, DnsRouteTarget, DnsRouteSubscription, DnsRouteTunnelInfo } from '$lib/types';
	import { Modal } from '$lib/components/ui';
	import { formatRelativeTime } from '$lib/utils/format';
	import DnsRouteDomainEditor from './DnsRouteDomainEditor.svelte';

	interface Props {
		open: boolean;
		route: DnsRoute | null;
		tunnels: DnsRouteTunnelInfo[];
		saving: boolean;
		onsave: (data: Partial<DnsRoute>) => void;
		onclose: () => void;
	}

	let { open, route, tunnels: rawTunnels, saving, onsave, onclose }: Props = $props();
	let tunnels = $derived(rawTunnels ?? []);

	// Form state
	let name = $state('');
	let manualDomains = $state<string[]>([]);
	let subscriptions = $state<DnsRouteSubscription[]>([]);
	let routes = $state<DnsRouteTarget[]>([]);
	let newSubUrl = $state('');

	let isInitialized = $state(false);

	// Reset form when modal opens
	$effect(() => {
		if (open) {
			if (!isInitialized) {
				if (route) {
					name = route.name;
					manualDomains = [...(route.manualDomains ?? [])];
					subscriptions = (route.subscriptions ?? []).map((s) => ({ ...s }));
					routes = (route.routes ?? []).map((r) => ({ ...r }));
				} else {
					name = '';
					manualDomains = [];
					subscriptions = [];
					// Auto-add the first available tunnel for new routes
					if (tunnels.length > 0) {
						const t = tunnels[0];
						routes = [{ interface: t.ndmsName, tunnelId: t.id, fallback: '' as const }];
					} else {
						routes = [];
					}
				}
				newSubUrl = '';
				newRouteTunnelId = '';
				isInitialized = true;
			}
		} else {
			isInitialized = false;
		}
	});

	// Computed
	let isEdit = $derived(route !== null);
	let title = $derived(isEdit ? `Редактирование: ${route?.name ?? ''}` : 'Новый DNS-маршрут');

	let totalDomains = $derived.by(() => {
		const manualCount = manualDomains.length;
		const subCount = subscriptions.reduce((acc, s) => acc + (s.lastCount ?? 0), 0);
		return manualCount + subCount;
	});

	let groupCount = $derived(Math.ceil(totalDomains / 300) || 0);

	let canSave = $derived(name.trim() !== '' && routes.length > 0);

	// Handlers
	function handleDomainsChange(domains: string[]) {
		manualDomains = domains;
	}

	function addSubscription() {
		const url = newSubUrl.trim();
		if (!url || !url.startsWith('http')) return;
		if (subscriptions.some((s) => s.url === url)) return;
		subscriptions = [...subscriptions, { url, name: url }];
		newSubUrl = '';
	}

	function removeSubscription(index: number) {
		subscriptions = subscriptions.filter((_, i) => i !== index);
	}

	let availableTunnels = $derived(tunnels.filter((t) =>
		!routes.some((r) => r.tunnelId === t.id) &&
		(!t.wan || t.status === 'up')
	));
	let newRouteTunnelId = $state('');

	function addRoute() {
		const tunnelId = newRouteTunnelId || availableTunnels[0]?.id;
		if (!tunnelId) return;
		const tunnel = tunnels.find((t) => t.id === tunnelId);
		if (!tunnel) return;
		// Move fallback from old last route to the new one
		const fallback = currentFallback;
		const cleared = routes.map((r) => ({ ...r, fallback: '' as const }));
		routes = [...cleared, { interface: tunnel.ndmsName, tunnelId: tunnel.id, fallback }];
		newRouteTunnelId = '';
	}

	function removeRoute(index: number) {
		const fallback = currentFallback;
		const updated = routes.filter((_, i) => i !== index);
		// Ensure fallback stays on the last route
		routes = updated.map((r, i) => ({
			...r,
			fallback: i === updated.length - 1 ? fallback : ''
		}));
	}

	function moveRoute(index: number, direction: number) {
		const target = index + direction;
		if (target < 0 || target >= routes.length) return;
		// Capture current fallback before swap (it lives on the last route)
		const fallback = currentFallback;
		const updated = [...routes];
		[updated[index], updated[target]] = [updated[target], updated[index]];
		// Fallback always belongs on the last route only
		routes = updated.map((r, i) => ({
			...r,
			fallback: i === updated.length - 1 ? fallback : ''
		}));
	}

	function tunnelName(tunnelId: string): string {
		return tunnels.find((t) => t.id === tunnelId)?.name ?? tunnelId;
	}

	function handleFallbackChange(value: string) {
		if (routes.length === 0) return;
		const fallback: DnsRouteTarget['fallback'] = (value === 'auto' || value === 'reject') ? value : '';
		routes = routes.map((r, i) =>
			i === routes.length - 1 ? { ...r, fallback } : r
		);
	}

	let currentFallback = $derived.by(() => {
		if (routes.length === 0) return '';
		return routes[routes.length - 1].fallback ?? '';
	});

	function handleSave() {
		const data: Partial<DnsRoute> = {
			name: name.trim(),
			manualDomains,
			subscriptions,
			routes
		};
		onsave(data);
	}

	function handleSubKeydown(e: KeyboardEvent) {
		if (e.key === 'Enter') {
			e.preventDefault();
			addSubscription();
		}
	}
</script>

<Modal {open} {title} size="lg" onclose={onclose}>
	<!-- Name -->
	<div class="form-group">
		<!-- svelte-ignore a11y_label_has_associated_control -->
		<label class="form-label">Название</label>
		<input
			class="form-input"
			type="text"
			placeholder="Заблокированные сайты"
			value={name}
			oninput={(e) => { name = (e.target as HTMLInputElement).value; }}
		/>
	</div>

	<!-- Manual domains -->
	<div class="form-section">
		<div class="section-title">Домены (вручную)</div>
		<DnsRouteDomainEditor domains={manualDomains} onchange={handleDomainsChange} />
	</div>

	<!-- Subscriptions -->
	<div class="form-section">
		<div class="section-title">Подписки</div>
		{#if subscriptions.length > 0}
			<div class="sub-list">
				{#each subscriptions as sub, i (sub.url)}
					<div class="sub-item">
						<div class="sub-info">
							<span class="sub-url">{sub.url}</span>
							<span class="sub-meta">
								{#if sub.lastError}
									<span class="sub-error">Ошибка: {sub.lastError}</span>
								{:else if sub.lastCount !== undefined && sub.lastCount > 0}
									<span class="sub-ok">{sub.lastCount} доменов</span>
									{#if sub.lastFetched}
										<span class="sub-time"> &middot; {formatRelativeTime(sub.lastFetched)}</span>
									{/if}
								{/if}
							</span>
						</div>
						<button class="btn-remove" onclick={() => removeSubscription(i)} title="Удалить подписку">
							&times;
						</button>
					</div>
				{/each}
			</div>
		{/if}
		<div class="sub-add">
			<input
				class="form-input"
				type="url"
				placeholder="https://example.com/domains.txt"
				value={newSubUrl}
				oninput={(e) => { newSubUrl = (e.target as HTMLInputElement).value; }}
				onkeydown={handleSubKeydown}
			/>
			<button class="btn btn-sm btn-secondary" onclick={addSubscription} disabled={!newSubUrl.trim()}>
				Добавить
			</button>
		</div>
	</div>

	<!-- Route chain -->
	<div class="form-section">
		<div class="section-title">Маршрут (порядок = приоритет)</div>
		{#if routes.length === 0}
			<p class="route-hint">Добавьте хотя бы один туннель для маршрутизации</p>
		{/if}
		{#if routes.length > 0}
			<div class="route-list">
				{#each routes as target, i (target.tunnelId)}
					<div class="route-item">
						<span class="route-index">{i + 1}.</span>
						<span class="route-name">{tunnelName(target.tunnelId)}</span>
						<div class="route-actions">
							<button class="btn-move" onclick={() => moveRoute(i, -1)} disabled={i === 0} title="Вверх">&uarr;</button>
							<button class="btn-move" onclick={() => moveRoute(i, 1)} disabled={i === routes.length - 1} title="Вниз">&darr;</button>
							<button class="btn-remove" onclick={() => removeRoute(i)} title="Удалить">&times;</button>
						</div>
					</div>
				{/each}
			</div>
		{/if}
		{#if availableTunnels.length > 0}
			<div class="route-add">
				<select
					class="form-select"
					value={newRouteTunnelId || availableTunnels[0]?.id || ''}
					onchange={(e) => { newRouteTunnelId = (e.target as HTMLSelectElement).value; }}
				>
					{#each availableTunnels as tunnel}
						<option value={tunnel.id}>{tunnel.name}{tunnel.system ? ' (системный)' : ''}</option>
					{/each}
				</select>
				<button class="btn btn-sm btn-primary" onclick={addRoute}>+ Добавить</button>
			</div>
		{/if}

		{#if routes.length > 0}
			<div class="fallback-group">
				<!-- svelte-ignore a11y_label_has_associated_control -->
				<label class="form-label">Если все недоступны:</label>
				<div class="fallback-options">
					<label class="fallback-option">
						<input
							type="radio"
							name="fallback"
							value="auto"
							checked={currentFallback === 'auto'}
							onchange={() => handleFallbackChange('auto')}
						/>
						<span>провайдер</span>
					</label>
					<label class="fallback-option">
						<input
							type="radio"
							name="fallback"
							value="reject"
							checked={currentFallback === 'reject'}
							onchange={() => handleFallbackChange('reject')}
						/>
						<span>эксклюзивный</span>
					</label>
				</div>
			</div>
		{/if}
	</div>

	<!-- Summary -->
	{#if totalDomains > 0}
		<div class="summary">
			Итого: {totalDomains} доменов{#if groupCount > 1} &rarr; {groupCount} групп по 300{/if}
		</div>
	{/if}

	{#snippet actions()}
		<button class="btn btn-secondary" onclick={onclose}>Отмена</button>
		<button class="btn btn-primary" onclick={handleSave} disabled={!canSave || saving}>
			{saving ? 'Сохранение...' : 'Сохранить'}
		</button>
	{/snippet}
</Modal>

<style>
	.form-group {
		margin-bottom: 1rem;
	}

	.form-label {
		display: block;
		font-size: 0.8125rem;
		font-weight: 500;
		color: var(--text-primary);
		margin-bottom: 0.375rem;
	}

	.form-input,
	.form-select {
		width: 100%;
		padding: 0.375rem 0.625rem;
		border: 1px solid var(--border);
		border-radius: 6px;
		background: var(--bg-primary);
		color: var(--text-primary);
		font-size: 0.8125rem;
		box-sizing: border-box;
	}

	.form-input:focus,
	.form-select:focus {
		outline: none;
		border-color: var(--accent);
	}

	.form-section {
		margin-bottom: 1.25rem;
	}

	.section-title {
		font-size: 0.75rem;
		font-weight: 600;
		color: var(--text-muted);
		text-transform: uppercase;
		letter-spacing: 0.05em;
		margin-bottom: 0.5rem;
		padding-bottom: 0.375rem;
		border-bottom: 1px solid var(--border);
	}

	/* Subscriptions */
	.sub-list {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		margin-bottom: 0.5rem;
	}

	.sub-item {
		display: flex;
		align-items: flex-start;
		justify-content: space-between;
		gap: 0.5rem;
		padding: 0.5rem;
		background: var(--bg-secondary);
		border-radius: 6px;
	}

	.sub-info {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
		min-width: 0;
	}

	.sub-url {
		font-size: 0.75rem;
		color: var(--text-primary);
		font-family: ui-monospace, SFMono-Regular, 'SF Mono', Menlo, monospace;
		word-break: break-all;
	}

	.sub-meta {
		font-size: 0.6875rem;
		color: var(--text-muted);
	}

	.sub-ok {
		color: var(--success, #10b981);
	}

	.sub-error {
		color: var(--error, #ef4444);
	}

	.sub-time {
		color: var(--text-muted);
	}

	.sub-add {
		display: flex;
		gap: 0.5rem;
		align-items: center;
	}

	.sub-add .form-input {
		flex: 1;
	}

	/* Route chain */
	.route-list {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		margin-bottom: 0.5rem;
	}

	.route-item {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.route-index {
		font-size: 0.8125rem;
		color: var(--text-muted);
		font-weight: 500;
		width: 1.5rem;
		flex-shrink: 0;
	}

	.route-name {
		flex: 1;
		font-size: 0.8125rem;
		color: var(--text-primary);
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.route-actions {
		display: flex;
		gap: 0.25rem;
		flex-shrink: 0;
	}

	.route-add {
		display: flex;
		gap: 0.5rem;
		align-items: center;
	}

	.route-add .form-select {
		flex: 1;
	}

	.btn-move {
		background: none;
		border: 1px solid var(--border);
		color: var(--text-muted);
		font-size: 0.75rem;
		cursor: pointer;
		padding: 0.125rem 0.375rem;
		line-height: 1;
		border-radius: 4px;
	}

	.btn-move:hover:not(:disabled) {
		color: var(--accent);
		border-color: var(--accent);
	}

	.btn-move:disabled {
		opacity: 0.3;
		cursor: default;
	}

	.btn-remove {
		background: none;
		border: none;
		color: var(--text-muted);
		font-size: 1.25rem;
		cursor: pointer;
		padding: 0 0.375rem;
		line-height: 1;
		border-radius: 4px;
		flex-shrink: 0;
	}

	.btn-remove:hover {
		color: var(--error, #ef4444);
		background: rgba(239, 68, 68, 0.1);
	}

	/* Fallback */
	.fallback-group {
		margin-top: 0.75rem;
	}

	.fallback-options {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem 1rem;
	}

	@media (max-width: 480px) {
		.fallback-options {
			flex-direction: column;
			gap: 0.5rem;
			align-items: flex-start;
		}
	}

	.fallback-option {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
		font-size: 0.8125rem;
		color: var(--text-primary);
		cursor: pointer;
		white-space: nowrap;
	}

	.fallback-option input[type="radio"] {
		accent-color: var(--accent);
	}

	.route-hint {
		font-size: 0.75rem;
		color: var(--warning, #eab308);
		margin: 0 0 0.5rem 0;
	}

	/* Summary */
	.summary {
		font-size: 0.8125rem;
		color: var(--text-muted);
		padding: 0.5rem 0;
		border-top: 1px dashed var(--border);
	}
</style>
