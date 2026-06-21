<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { api } from '$lib/api/client';
	import { monitoringStore } from '$lib/stores/monitoring';
	import { LoadingSpinner, EmptyState } from '$lib/components/layout';
	import { SideDrawer, Toggle } from '$lib/components/ui';
	import { MatrixGrid, MatrixStatusStrip, MatrixDrillDown } from '$lib/components/monitoring';
	import { notifications } from '$lib/stores/notifications';
	import type { MonitoringTarget, MonitoringTunnel, Settings } from '$lib/types';

	let drawerOpen = $state(false);
	let drawerTarget = $state<MonitoringTarget | null>(null);
	let drawerTunnel = $state<MonitoringTunnel | null>(null);
	let refreshing = $state(false);
	const AUTO_REFRESH_MS = 60_000;
	let nowTs = $state(Date.now());
	let lastRefreshTs = $state(0);
	let lastFetchedAtTs = $state(0);
	let nextAutoRefreshTs = $state(0);
	let autoRefreshTimeout: ReturnType<typeof setTimeout> | null = null;
	let progressTimer: ReturnType<typeof setInterval> | null = null;
	let autoPressResetTimer: ReturnType<typeof setTimeout> | null = null;
	let autoPressActive = $state(false);
	let settings = $state<Settings | null>(null);
	let monitoringEnabled = $state(true);
	let excludedTunnelIds = $state<Set<string>>(new Set());
	let excludedTunnelNames = $state<Record<string, string>>({});
	let excludedNamesReady = $state(false);
	let excludedTunnelList = $derived.by(() => [...excludedTunnelIds].sort((a, b) => a.localeCompare(b)));
	let excludedTunnelLabels = $derived.by(() =>
		excludedTunnelList.map((id) => ({
			id,
			name: (excludedTunnelNames[id]?.trim() || (excludedNamesReady ? id : '')).trim(),
		})).filter((item) => item.name !== ''),
	);
	const updatedTimeLabel = $derived.by(() => {
		if (lastFetchedAtTs <= 0) return '';
		const d = new Date(lastFetchedAtTs);
		if (Number.isNaN(d.getTime())) return '';
		return d.toLocaleTimeString('ru-RU', { hour: '2-digit', minute: '2-digit', second: '2-digit' });
	});
	const refreshProgress = $derived.by(() => {
		if (lastRefreshTs <= 0 || nextAutoRefreshTs <= lastRefreshTs) return 0;
		const elapsed = Math.max(0, nowTs - lastRefreshTs);
		const cycle = nextAutoRefreshTs - lastRefreshTs;
		return Math.min(1, elapsed / cycle);
	});

	function normalizeExcludedTunnelId(raw: string): string {
		let id = (raw ?? '').trim();
		// Backward-compat: tolerate legacy malformed values:
		// "\"tn-1\"", ["tn-1"], ["tn-1" and "tn-1"], etc.
		let prev = '';
		while (id !== prev) {
			prev = id;
			id = id
				.trim()
				.replace(/^[\s\["',]+/g, '')
				.replace(/[\s\]"',]+$/g, '');
		}
		return id;
	}

	function normalizeExcludedTunnelIds(raw: string[] | undefined): Set<string> {
		const out = new Set<string>();
		for (const item of raw ?? []) {
			const trimmed = item.trim();
			if (!trimmed) continue;
			// Legacy defensive path: in some buggy states the whole JSON array
			// may be persisted as a single string element.
			if (trimmed.startsWith('[') && trimmed.endsWith(']')) {
				try {
					const parsed = JSON.parse(trimmed);
					if (Array.isArray(parsed)) {
						for (const v of parsed) {
							const normalized = normalizeExcludedTunnelId(String(v));
							if (normalized) out.add(normalized);
						}
						continue;
					}
				} catch {
					// Fall through to plain sanitizer below.
				}
			}
			const normalized = normalizeExcludedTunnelId(trimmed);
			if (normalized) out.add(normalized);
		}
		return out;
	}

	function triggerRefresh(force = false): void {
		void refresh(force);
	}

	function triggerAutoRefresh(): void {
		autoPressActive = true;
		if (autoPressResetTimer) clearTimeout(autoPressResetTimer);
		autoPressResetTimer = setTimeout(() => {
			autoPressActive = false;
			autoPressResetTimer = null;
		}, 220);
		triggerRefresh(true);
	}

	async function refresh(force = false) {
		if (refreshing) {
			if (autoRefreshTimeout) clearTimeout(autoRefreshTimeout);
			autoRefreshTimeout = setTimeout(() => {
				triggerAutoRefresh();
			}, AUTO_REFRESH_MS);
			return;
		}
		refreshing = true;
		try {
			const snap = await api.getMonitoringMatrix({ force });
			monitoringStore.setSnapshot(snap);
			const names = { ...excludedTunnelNames };
			for (const t of snap.tunnels) {
				names[t.id] = t.name || t.id;
			}
			excludedTunnelNames = names;
			void hydrateExcludedTunnelNames();
			lastFetchedAtTs = Date.now();
		} catch {
			// Suppress error notification when cached data is visible — the user
			// sees stale data and the next auto-refresh will retry silently.
			// Show the error only when there is nothing at all to display.
			if (!$monitoringStore.snapshot) {
				notifications.error('Не удалось загрузить матрицу мониторинга');
			}
		} finally {
			lastRefreshTs = Date.now();
			nextAutoRefreshTs = lastRefreshTs + AUTO_REFRESH_MS;
			if (autoRefreshTimeout) clearTimeout(autoRefreshTimeout);
			autoRefreshTimeout = setTimeout(() => {
				triggerAutoRefresh();
			}, AUTO_REFRESH_MS);
			refreshing = false;
		}
	}

	async function loadSettings() {
		try {
			settings = await api.getSettings();
			monitoringEnabled = settings.monitoring?.enabled ?? true;
			excludedNamesReady = false;
			excludedTunnelIds = normalizeExcludedTunnelIds(settings.monitoringExcludedTunnels);
			const names: Record<string, string> = {};
			for (const t of $monitoringStore.snapshot?.tunnels ?? []) {
				names[t.id] = t.name || t.id;
			}
			excludedTunnelNames = names;
			await hydrateExcludedTunnelNames();
		} catch {
			// Monitoring matrix can still work without settings payload.
		}
	}

	async function hydrateExcludedTunnelNames() {
		const missing = [...excludedTunnelIds].filter((id) => !excludedTunnelNames[id]);
		if (missing.length === 0) {
			excludedNamesReady = true;
			return;
		}
		const map: Record<string, string> = { ...excludedTunnelNames };
		const setName = (id: string, candidate: string) => {
			const key = normalizeExcludedTunnelId(id);
			const name = candidate.trim();
			if (!key || !name) return;
			const existing = map[key];
			// Never replace a meaningful display name with a technical id/tag.
			if (!existing || existing === key) {
				map[key] = name;
			}
		};
		try {
			const [awg, system, singbox, subscriptions] = await Promise.all([
				api.listTunnels().catch(() => []),
				api.listSystemTunnels().catch(() => []),
				api.singboxListTunnels().catch(() => []),
				api.listSubscriptions().catch(() => []),
			]);
			for (const t of awg) setName(t.id, t.name || t.id);
			for (const t of system) {
				const label = t.description || t.id;
				setName(t.id, label);
				setName(`sys-${t.id}`, label);
			}
			for (const sub of subscriptions) {
				const label = sub.label?.trim() || sub.selectorTag?.trim() || sub.id;
				setName(sub.activeMember, label);
				for (const tag of sub.memberTags ?? []) setName(tag, label);
				for (const member of sub.members ?? []) setName(member.tag, label);
			}
			for (const t of singbox) setName(t.tag, t.tag);
			excludedTunnelNames = map;
		} catch {
			// Best-effort UX enrichment only.
		} finally {
			excludedNamesReady = true;
		}
	}

	onMount(() => {
		monitoringStore.loadCached();
		void loadSettings();
		triggerAutoRefresh();
		progressTimer = setInterval(() => {
			nowTs = Date.now();
		}, 200);
	});

	async function setMonitoringEnabled(next: boolean) {
		monitoringEnabled = next; // optimistic
		try {
			settings = await api.updateSettings({ monitoring: { enabled: next } });
			monitoringEnabled = settings.monitoring?.enabled ?? next;
		} catch {
			monitoringEnabled = !next; // rollback
			notifications.error('Не удалось переключить наблюдение');
		}
	}

	async function toggleTunnelExcluded(tunnelId: string, excluded: boolean, tunnelName = '') {
		if (!settings) {
			await loadSettings();
			if (!settings) return;
		}
		const previous = new Set(excludedTunnelIds);
		const next = new Set(excludedTunnelIds);
		const normalizedId = normalizeExcludedTunnelId(tunnelId);
		if (!normalizedId) return;
		if (excluded) next.add(normalizedId);
		else next.delete(normalizedId);
		excludedTunnelIds = next;
		if (excluded && tunnelName.trim() !== '') {
			excludedTunnelNames = { ...excludedTunnelNames, [normalizedId]: tunnelName };
		}
		try {
			settings = await api.updateSettings({ monitoringExcludedTunnels: [...next] });
			excludedTunnelIds = normalizeExcludedTunnelIds(settings.monitoringExcludedTunnels);
			await refresh(true);
			await hydrateExcludedTunnelNames();
		} catch (e) {
			excludedTunnelIds = previous;
			notifications.error(e instanceof Error ? e.message : 'Не удалось обновить исключения мониторинга');
		}
	}

	onDestroy(() => {
		if (autoRefreshTimeout) clearTimeout(autoRefreshTimeout);
		if (progressTimer) clearInterval(progressTimer);
		if (autoPressResetTimer) clearTimeout(autoPressResetTimer);
	});

	function openCell(target: MonitoringTarget, tunnel: MonitoringTunnel) {
		drawerTarget = target;
		drawerTunnel = tunnel;
		drawerOpen = true;
	}

	function closeDrawer() {
		drawerOpen = false;
	}
</script>

<div class="meta-row">
	<div class="meta-left">
		{#if updatedTimeLabel && !$monitoringStore.stale}
			<span class="updated-clock">
				<span class="clock-dot" class:clock-dot-loading={refreshing}></span>
				Обновлено: {updatedTimeLabel}
			</span>
		{/if}
	</div>
	<div class="meta-actions">
		{#if $monitoringStore.stale && refreshing}
			<span class="stale-badge">обновляется...</span>
		{/if}
		<button
			type="button"
			class="refresh-btn timer-enabled"
			class:auto-press={autoPressActive}
			onclick={() => triggerRefresh(true)}
			disabled={refreshing}
			aria-label="Обновить мониторинг"
			title="Обновить"
			style={`--refresh-progress:${refreshProgress * 360}deg;`}
		>
			<svg class="refresh-icon" viewBox="0 0 24 24" fill="none" aria-hidden="true">
				<path d="M21 12a9 9 0 1 1-2.64-6.36M21 4v6h-6" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
			</svg>
			<span>Обновить</span>
		</button>
	</div>
</div>

<div class="master-row">
	<span class="master-label">Наблюдение по таргетам</span>
	<Toggle checked={monitoringEnabled} onchange={setMonitoringEnabled} />
</div>

{#if !monitoringEnabled}
	<EmptyState
		title="Наблюдение выключено"
		description="Включите, чтобы собирать матрицу и историю по таргетам."
	/>
{:else if $monitoringStore.snapshot}
	{#if excludedTunnelList.length > 0 && excludedNamesReady}
		<div class="excluded-strip">
			<span class="excluded-label">Исключены из мониторинга:</span>
			<div class="excluded-items">
				{#each excludedTunnelLabels as item}
					<button
						type="button"
						class="excluded-chip"
						onclick={() => toggleTunnelExcluded(item.id, false)}
						title="Вернуть в мониторинг"
					>
						{item.name}
					</button>
				{/each}
			</div>
		</div>
	{/if}
	<MatrixStatusStrip snapshot={$monitoringStore.snapshot} />
	<MatrixGrid
		snapshot={$monitoringStore.snapshot}
		onCellClick={openCell}
		excludedTunnelIds={excludedTunnelIds}
		onToggleTunnelExcluded={toggleTunnelExcluded}
	/>
{:else if !$monitoringStore.loaded}
	<div class="loading"><LoadingSpinner size="lg" message="Получение данных мониторинга..." /></div>
{:else}
	<EmptyState
		title="Нет данных мониторинга"
		description="Запустите хотя бы один туннель и подождите ~60 секунд для первого тика probe scheduler'а."
	/>
{/if}

<SideDrawer
	open={drawerOpen}
	onClose={closeDrawer}
	title={drawerTarget && drawerTunnel ? `${drawerTarget.name} × ${drawerTunnel.name}` : ''}
>
	{#if drawerTarget && drawerTunnel}
		<MatrixDrillDown target={drawerTarget} tunnel={drawerTunnel} onClose={closeDrawer} />
	{/if}
</SideDrawer>

<style>
	.meta-row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
		margin-bottom: 1rem;
		min-height: 28px;
	}
	.meta-left {
		display: inline-flex;
		align-items: center;
		min-height: 28px;
	}

	.meta-actions {
		display: inline-flex;
		align-items: center;
		gap: 0.5rem;
	}

	.stale-badge {
		font-size: 11px;
		color: var(--color-text-muted);
		opacity: 0.7;
		font-style: italic;
	}

	.updated-clock {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
		font-family: var(--font-mono, ui-monospace, monospace);
		font-size: 11px;
		color: var(--color-text-muted);
	}

	.clock-dot {
		width: 7px;
		height: 7px;
		border-radius: 50%;
		background: var(--color-success);
		box-shadow: 0 0 0 3px var(--color-success-tint);
		transition: background 0.2s ease;
	}

	.clock-dot-loading {
		background: var(--color-warning, var(--color-accent));
		animation: pulse 1s ease-in-out infinite;
	}

	.refresh-btn {
		position: relative;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		gap: 0.4rem;
		height: 28px;
		padding: 0 0.625rem;
		border-radius: 6px;
		border: 1px solid var(--color-border);
		background: transparent;
		color: var(--color-text-muted);
		font-size: 0.8125rem;
		font-weight: 500;
		cursor: pointer;
		transition: all var(--t-fast) ease;
	}

	.refresh-btn.timer-enabled::before {
		content: '';
		position: absolute;
		inset: -1px;
		border-radius: inherit;
		padding: 1px;
		background: conic-gradient(var(--color-accent) var(--refresh-progress), transparent 0deg);
		-webkit-mask: linear-gradient(#000 0 0) content-box, linear-gradient(#000 0 0);
		mask: linear-gradient(#000 0 0) content-box, linear-gradient(#000 0 0);
		-webkit-mask-composite: xor;
		mask-composite: exclude;
		pointer-events: none;
		opacity: 0.95;
	}

	.refresh-btn:hover:not(:disabled) {
		color: var(--color-accent);
		background: var(--color-bg-hover);
	}

	.refresh-btn.auto-press:not(:disabled) {
		color: var(--color-accent);
		background: var(--color-bg-hover);
		transform: translateY(1px);
	}

	.refresh-btn:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.refresh-icon {
		position: relative;
		z-index: 1;
		width: 14px;
		height: 14px;
	}

	.excluded-strip {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.5rem;
		margin-bottom: 0.75rem;
		padding: 0.625rem 0.75rem;
		border: 1px dashed var(--color-border);
		border-radius: var(--radius-md);
		background: color-mix(in srgb, var(--color-bg-secondary) 70%, transparent);
	}
	.excluded-label {
		font-size: 12px;
		color: var(--color-text-muted);
	}
	.excluded-items {
		display: inline-flex;
		flex-wrap: wrap;
		gap: 0.375rem;
	}
	.excluded-chip {
		height: 24px;
		padding: 0 0.55rem;
		border-radius: 999px;
		border: 1px solid color-mix(in srgb, var(--color-error) 35%, var(--color-border));
		background: color-mix(in srgb, var(--color-error) 10%, transparent);
		color: var(--color-text-primary);
		font-size: 11px;
		cursor: pointer;
		transition: background var(--t-fast) ease;
	}
	.excluded-chip:hover {
		background: color-mix(in srgb, var(--color-success) 14%, transparent);
	}

	.loading {
		display: flex;
		justify-content: center;
		padding: 4rem 0;
	}
</style>
