<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { api } from '$lib/api/client';
	import { singboxRouter } from '$lib/stores/singboxRouter';
	import { singboxTunnels } from '$lib/stores/singbox';
	import { singboxProxies, latencyHistory } from '$lib/stores/singboxProxies';
	import { StatRow, Badge } from '$lib/components/ui';
	import type { StatTile } from '$lib/components/ui';
	import type { AWGTagInfo, SingboxTunnel } from '$lib/types';
	import {
		buildOutboundOptions,
		CompositeOutboundsList,
	} from '$lib/components/routing/singboxRouter';

	const outboundsStore = singboxRouter.outbounds;
	const phase1Store = singboxTunnels;

	const outbounds = $derived($outboundsStore);
	const phase1Tunnels = $derived(($phase1Store.data ?? []) as SingboxTunnel[]);
	const proxiesGroups = $derived($singboxProxies.data ?? []);
	const proxiesHistory = $derived($latencyHistory);

	let awgTags = $state<AWGTagInfo[]>([]);

	async function loadAWGTags(): Promise<void> {
		try {
			awgTags = await api.getAWGTags();
		} catch {
			awgTags = [];
		}
	}

	async function refresh(): Promise<void> {
		await singboxRouter.loadAll();
	}

	let unsubProxies: (() => void) | null = null;

	onMount(() => {
		loadAWGTags();
		void refresh();
		unsubProxies = singboxProxies.subscribe(() => {});
	});

	onDestroy(() => {
		unsubProxies?.();
	});

	const outboundOptions = $derived(
		// Composite members must reference real declared outbounds/tunnels.
		// Do not offer synthetic "direct" here.
		buildOutboundOptions(awgTags, phase1Tunnels, outbounds, false),
	);

	const awgManagedCount = $derived(
		awgTags.filter((t) => t.kind === 'managed').length,
	);
	const awgSystemCount = $derived(
		awgTags.filter((t) => t.kind === 'system').length,
	);

	// Sort: managed first, then system; stable alphabetical inside each group.
	const sortedAwgTags = $derived(
		[...awgTags].sort((a, b) => {
			if (a.kind !== b.kind) return a.kind === 'managed' ? -1 : 1;
			return a.tag.localeCompare(b.tag);
		}),
	);

	// Total addressable outbounds available as routing targets:
	// composite + AWG managed + AWG system + sing-box phase1 tunnels.
	// Mirrors what `buildOutboundOptions` exposes (minus the synthetic "direct").
	const totalCount = $derived(
		outbounds.length + awgTags.length + phase1Tunnels.length,
	);

	const statTiles = $derived<StatTile[]>([
		{ label: 'Composite', value: outbounds.length },
		{ label: 'AWG', value: awgManagedCount },
		{ label: 'Система', value: awgSystemCount },
		{ label: 'Всего', value: totalCount },
	]);
</script>

<div class="stat-row-wrap">
	<StatRow tiles={statTiles} columns={4} />
</div>

<section class="auto-section">
	<div class="section-head">
		<h3 class="section-title">Авто-сгенерированные</h3>
		<p class="section-hint">
			Создаются автоматически из туннелей. Управление — в разделе Туннели и Системные туннели.
		</p>
	</div>

	<div class="grid">
		{#if sortedAwgTags.length === 0}
			<div class="empty">
				Нет туннелей. Создайте AWG-туннель или системный NativeWG, чтобы они появились здесь.
			</div>
		{:else}
			{#each sortedAwgTags as t (t.tag)}
				<div class="card">
					<div class="card-head">
						<div class="tag mono" title={t.tag}>{t.tag}</div>
						<Badge
							variant={t.kind === 'managed' ? 'accent' : 'info'}
							size="sm"
							uppercase
						>
							{t.kind === 'managed' ? 'AWG' : 'Система'}
						</Badge>
					</div>

					<div class="meta-row">
						<div class="meta-label">Label</div>
						<div class="meta-value" title={t.label}>{t.label || '—'}</div>
					</div>

					<div class="meta-row">
						<div class="meta-label">Iface</div>
						<div class="meta-value mono" title={t.iface}>{t.iface || '—'}</div>
					</div>
				</div>
			{/each}
		{/if}
	</div>
</section>

<CompositeOutboundsList
	{outbounds}
	{outboundOptions}
	onChange={refresh}
	proxies={proxiesGroups}
	latencyHistory={proxiesHistory}
/>

<style>
	.stat-row-wrap {
		margin-bottom: 1rem;
	}
	.auto-section {
		margin-bottom: 1.5rem;
	}
	.section-head {
		margin-bottom: 0.625rem;
	}
	.section-title {
		font-size: 0.9375rem;
		font-weight: 600;
		color: var(--color-text-primary);
		margin: 0 0 0.25rem 0;
	}
	.section-hint {
		font-size: 0.8125rem;
		font-style: italic;
		color: var(--color-text-muted);
		margin: 0;
	}
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
		gap: 0.75rem;
	}
	.card {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		padding: 0.875rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
	}
	.card-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
	}
	.tag {
		font-size: 0.875rem;
		font-weight: 600;
		color: var(--color-text-primary);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		min-width: 0;
	}
	.meta-row {
		display: grid;
		grid-template-columns: 60px 1fr;
		gap: 0.5rem;
		align-items: center;
		font-size: 0.8125rem;
	}
	.meta-label {
		color: var(--color-text-muted);
		font-size: 0.6875rem;
		text-transform: uppercase;
		letter-spacing: 0.05em;
	}
	.meta-value {
		color: var(--color-text-secondary);
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.mono {
		font-family: var(--font-mono, ui-monospace, monospace);
	}
	.empty {
		grid-column: 1 / -1;
		padding: 1.25rem;
		text-align: center;
		color: var(--color-text-muted);
		font-size: 0.8125rem;
		border: 1px dashed var(--color-border);
		border-radius: var(--radius);
	}
</style>
